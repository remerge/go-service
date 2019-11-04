package registry

import (
	"container/list"
	"fmt"
	"reflect"
	"strings"

	"github.com/remerge/cue"
)

type Registrar interface {
	Register(interface{}, ...interface{}) (func(...interface{}) (interface{}, error), error)
}

// Registry is used to register service  constructors and instantiate the services.
// It provides a tools for dependency inject based service composition.
type Registry struct {
	providers map[reflect.Type]*provider
	log       cue.Logger
}

// New create a new Registry
func New() *Registry {
	return &Registry{
		providers: make(map[reflect.Type]*provider),
		log:       cue.NewLogger("registry"),
	}
}

// Params is used to mark structs as ctor parameter holder
type Params struct{}

var paramsType = reflect.TypeOf(Params{})

type provider struct {
	requires                []reflect.Type
	expectedParamStruct     reflect.Type
	requiresOnInstantiation []reflect.Type
	provides                reflect.Type
	ctor                    reflect.Value
	instance                *reflect.Value
	staticArgs              []interface{}
}

// Register registers a component constructor function with the registry. The constructor function can
// have zero or more parameters. If it has parameters these are treated as requirement for the ctor. The ctor functions
// return signature is used to infer which type is created by the ctor. The second return value is the error.
// Parameters to the ctor are resolve by the registry. If a parameters type was not registered with the registry before
// the instantiation will fail. There are two exceptions to this rule:
// 1) if the function signature has a single parameter with a struct type that embeds the Params struct. In this case the structs members
//    are used as requirements for the ctor. This helps with ctor that require a large number of dependencies
// 2) If there is an exact sub signature match with parameters passed to Request
func (r *Registry) Register(ctor interface{}, args ...interface{}) (func(...interface{}) (interface{}, error), error) {
	t := reflect.TypeOf(ctor)

	if t.Kind() != reflect.Func {
		return nil, fmt.Errorf("Register expects only functions as parameter, a %v was passed", t.Kind())
	}

	if t.NumOut() != 2 {
		return nil, fmt.Errorf("Register expects a function that return two values not %d", t.NumOut())
	}

	provided := t.Out(0)

	if _, found := r.providers[provided]; found {
		return nil, fmt.Errorf("A provider for %v was already registered before", provided)
	}

	p := &provider{
		provides:   provided,
		ctor:       reflect.ValueOf(ctor),
		staticArgs: args,
	}

	if t.NumIn() == 1 && embedsType(t.In(0), paramsType) {
		// if the constructor only has one parameter and that parameter embeds
		// the type paramType we assume it is a struct that wraps parameters
		p.expectedParamStruct = t.In(0)
		pt := t.In(0)
		if pt.Kind() == reflect.Ptr {
			pt = pt.Elem()
		}
		for i := 0; i < pt.NumField(); i++ {
			f := pt.Field(i)
			isLazyParam, _ := getRegistryTags(f)
			if f.PkgPath == "" && f.Type != paramsType && !isLazyParam {
				p.requires = append(p.requires, f.Type)
			}
			if isLazyParam {
				p.requiresOnInstantiation = append(p.requiresOnInstantiation, f.Type)
			}
		}
	} else {
		for i := 0; i < t.NumIn(); i++ {
			p.requires = append(p.requires, t.In(i))
		}
	}
	r.log.Debugf("registered provider for %v, requires=%v requiresOnInstantiation=%v", p.provides, p.requires, p.requiresOnInstantiation)
	r.providers[p.provides] = p
	resolvedCtor := &ResolvedCtor{p, r}
	return resolvedCtor.Call, nil
}

// Request resolves a dependency tree for a given target type and sets up all objects on the way and creates an instance of targetType
// targetType is the type of the requested object
// params can be used to pass additional parameter structs.
func (r *Registry) Request(targetType reflect.Type, params ...interface{}) (interface{}, error) {
	provider, err := r.providerFor(targetType)
	if err != nil {
		return nil, err
	}
	return r.interfaceFor(provider, params)
}

// RequestAndSet resolves a dependency tree of a given target and sets up all objects on the way.
// target needs to be a pointer to a pointer to the struct that should be initialized.
// params can be used to pass additional parameter structs
func (r *Registry) RequestAndSet(target interface{}, params ...interface{}) error {
	// must be a pointer to a pointer
	pt := reflect.TypeOf(target)

	if pt.Kind() != reflect.Ptr {
		return fmt.Errorf("Target needs to be a pointer to a pointer but is %v", pt)
	}

	// can we provide what is requested?
	t := reflect.ValueOf(target).Elem().Type()

	// needs to be a pointer OR an interface
	if t.Kind() != reflect.Ptr && t.Kind() != reflect.Interface {
		return fmt.Errorf("Dereferenced target needs to be a pointer but is %v", t)
	}

	v, err := r.Request(t, params...)
	if err != nil {
		return err
	}
	holder := reflect.ValueOf(target).Elem()
	holder.Set(reflect.ValueOf(v))
	return nil
}

func (r *Registry) interfaceFor(p *provider, params []interface{}) (interface{}, error) {
	if p.instance == nil {

		// join in any provider static args
		for _, arg := range p.staticArgs {
			params = append(params, arg)
		}

		if err := r.resolve(p, params); err != nil {
			r.log.Debugf("could not resolve %v params=%v err=%v", p.ctor.Type(), params, err)
			return nil, err
		}
	}
	return p.instance.Interface(), nil
}

func (r *Registry) providerFor(t reflect.Type) (*provider, error) {
	r.log.Debugf("requesting provider for %v", t)
	provider, found := r.providers[t]
	if !found {
		p, err := r.findProviderForInterface(t)
		if err != nil {
			return nil, err
		}
		if p != nil {
			r.log.Debugf("%v is an interface provided by %v", t, p.provides)
		}
		provider = p
	}
	if provider == nil {
		return nil, fmt.Errorf("no provider for %v", t)
	}
	return provider, nil
}

// resolve is recursive - it doesn't build a proper graph at the moment
// This should be sufficient for our usecases at the moment
func (r *Registry) resolve(p *provider, extraParams []interface{}) error {
	r.log.Debugf("resolving %v, requires=%v extraParams=%v", p.provides, p.requires, extraParams)

	if p.instance != nil {
		r.log.Debugf("returning previously created instance=%v", p.instance)
		return nil
	}

	var params []reflect.Value
	var filteredExtraParams []interface{}

	for _, param := range extraParams {
		for _, requiredOnInstantiation := range p.requiresOnInstantiation {
			if requiredOnInstantiation == reflect.TypeOf(param) {
				filteredExtraParams = append(filteredExtraParams, param)
			}
		}
	}

	for idx, t := range p.requires {
		r.log.Debugf("walking requires for %v require=%v extraParams=%v", p.ctor.Type(), t, extraParams)
		provider, found := r.providers[t]
		if !found {
			r.log.Debugf("no direct provider for %v, is interface=%t (kind=%v)", t, t.Kind() == reflect.Interface, t.Kind())
			// t might be an interface, lets scan all provider - maybe there is one that implements it?
			var err error
			provider, err = r.findProviderForInterface(t)
			if err != nil {
				return err
			}
		}
		if provider == nil {
			// t was not an interface or no provided type implements t
			// we support top level direct params, but they need to map exactly (order and types)!
			// this is a special case and we will terminate the loop for this
			if !exactSubSignatureMatch(p.ctor.Type(), idx, extraParams) {
				return fmt.Errorf("no provider for %v (and no exact signature match), required by %v", t, p.ctor.Type())
			}
			r.log.Debugf("exact subtype match %v idx=%v extraParams=%v", p.ctor.Type(), idx, extraParams)
			filteredExtraParams = extraParams
			break
		}
		if err := r.resolve(provider, extraParams); err != nil {
			return err
		}
		params = append(params, *provider.instance)
	}

	// lets attach all extra params
	for _, p := range filteredExtraParams {
		params = append(params, reflect.ValueOf(p))
	}
	r.log.Debugf("filtered extra params are %v ", filteredExtraParams)

	return r.instantiate(p, params)
}

func (r *Registry) findProviderForInterface(t reflect.Type) (p *provider, err error) {
	if t.Kind() != reflect.Interface {
		return nil, nil
	}
	var implementor reflect.Type
	for providedType, provider := range r.providers {
		// r.log.Debugf("%v implements %v = %t", providedType, t, providedType.Implements(t))
		if providedType.Implements(t) {
			if implementor != nil {
				// we only support one type implementing a interface parameter per registry
				return nil, fmt.Errorf("can not pick corect implementor. Multiple types(%v and %v) implement the requested interface %v", implementor, providedType, t)
			}
			implementor = providedType
			p = provider
		}
	}
	return p, nil
}

func mapToValue(values []interface{}) (r []reflect.Value) {
	for _, i := range values {
		r = append(r, reflect.ValueOf(i))
	}
	return r
}

func (r *Registry) instantiate(p *provider, params []reflect.Value) error {
	r.log.Debugf("instantiate %v with %v", p.provides, params)

	if p.expectedParamStruct != nil {
		params = []reflect.Value{createParamStruct(p.expectedParamStruct, params)}
	}

	res := p.ctor.Call(params)
	if !res[1].IsNil() {
		return res[1].Interface().(error)
	}
	if res[0].IsNil() {
		return fmt.Errorf("The constructor %v return a nil value, this is not allowed", p.ctor.Type())
	}

	v := reflect.ValueOf(res[0].Interface())
	p.instance = &v
	return nil
}

// Ctor exposes the constructor function with a reference to the registry so it can be
// called without passing the registry again
type ResolvedCtor struct {
	p *provider
	r *Registry
}

// Call call a constructor directly, params need to match the signature exactly in this case.
func (ctor *ResolvedCtor) Call(params ...interface{}) (interface{}, error) {
	return ctor.r.interfaceFor(ctor.p, params)
}

func createParamStruct(t reflect.Type, params []reflect.Value) reflect.Value {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	paramsStructPtr := reflect.New(t)
	paramsStruct := paramsStructPtr.Elem()
	// not performance critical so we just do the o^2 approach
	for i := 0; i < paramsStruct.NumField(); i++ {
		f := paramsStruct.Field(i)
		if paramsStruct.Type().Field(i).PkgPath != "" {
			continue
		}
		if f.Type() == paramsType {
			continue
		}
		found := false
		for _, p := range params {
			// not 100% correct as we don't handle the case if multiple registered objects
			// implement the same interface - for now we can ignore this
			if p.Type() == f.Type() || (f.Kind() == reflect.Interface && p.Type().Implements(f.Type())) {
				f.Set(p)
				found = true
				break
			}
		}
		if !found {
			structField := paramsStruct.Type().Field(i)
			structFieldType := structField.Type
			if structFieldType.Kind() == reflect.Ptr {
				structFieldType = structFieldType.Elem()
			}
			isStruct := structFieldType.Kind() == reflect.Struct
			if !isStruct {
				panic("could not find struct param " + f.Type().String() + " for " + t.String())
			}
			// if it is a point we might allow nil values
			_, allowNil := getRegistryTags(structField)
			if !allowNil {
				panic("could not find struct param " + f.Type().String() + " for " + t.String())
			}
			if f.Type().Kind() != reflect.Ptr {
				panic("only param struct fields of type Pointer can be tagged as 'allownnil'. Type is " + f.Type().String() + " for " + t.String())
			}
			// we leave it at null value
		}
	}
	return paramsStructPtr
}

func embedsType(embedder, embedded reflect.Type) bool {
	types := list.New()
	types.PushBack(embedder)
	for types.Len() > 0 {
		t := types.Remove(types.Front()).(reflect.Type)
		if t == embedded {
			return true
		}

		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}

		if t.Kind() != reflect.Struct {
			continue
		}

		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				types.PushBack(f.Type)
			}
		}
	}
	return false
}

func exactSubSignatureMatch(ctorType reflect.Type, idx int, params []interface{}) bool {
	if ctorType.NumIn()-idx != len(params) {
		return false
	}
	for i := idx; i < ctorType.NumIn(); i++ {
		if ctorType.In(i) != reflect.TypeOf(params[i-idx]) {
			return false
		}
	}
	return true
}

func getRegistryTags(field reflect.StructField) (isLazy, allowNil bool) {
	tag, found := field.Tag.Lookup("registry")
	if !found || tag == "" {
		return false, false
	}
	isLazy = strings.Contains(tag, "lazy")
	allowNil = strings.Contains(tag, "allownil")
	return isLazy, allowNil
}
