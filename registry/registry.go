package registry

import (
	"container/list"
	"fmt"
	"reflect"

	"github.com/remerge/cue"
)

type ServiceRegistry struct {
	providers map[reflect.Type]*provider
	log       cue.Logger
}

func New() *ServiceRegistry {
	return &ServiceRegistry{
		providers: make(map[reflect.Type]*provider),
		log:       cue.NewLogger("registry"),
	}
}

type Params struct{}

var paramsType = reflect.TypeOf(Params{})

type provider struct {
	requires                []reflect.Type
	expectedParamStruct     reflect.Type
	requiresOnInstantiation []reflect.Type
	provides                reflect.Type
	ctor                    reflect.Value
	instance                *reflect.Value
}

// Register registers a services constructor function with the registry. The constructor function can
// have zero or more parameters. If it has parameters these are treated as requirement for the ctor. The ctor functions
// return signature is used to infer which type is created by the ctor. The second return value is the error if any.
// Parameters to the ctor are resolve by the registry. If a parameters type was not registered with the registry before
// the instantiation will fail.
func (sr *ServiceRegistry) Register(ctor interface{}) error {
	t := reflect.TypeOf(ctor)

	if t.Kind() != reflect.Func {
		return fmt.Errorf("Register expects only functions as parameter, a %v was passed", t.Kind())
	}

	if t.NumOut() != 2 {
		return fmt.Errorf("Register expects a function that return two values not %d", t.NumOut())
	}

	provided := t.Out(0)

	if _, found := sr.providers[provided]; found {
		return fmt.Errorf("A provider for %v was already registered before", provided)
	}

	// TODO: check types of return values
	p := &provider{
		provides: provided,
		ctor:     reflect.ValueOf(ctor),
	}

	if t.NumIn() == 1 && embedsType(t.In(0), paramsType) {
		p.expectedParamStruct = t.In(0)
		pt := t.In(0)
		if pt.Kind() == reflect.Ptr {
			pt = pt.Elem()
		}
		for i := 0; i < pt.NumField(); i++ {
			f := pt.Field(i)
			v, _ := f.Tag.Lookup("registry")
			isLazyParam := v == "lazy"
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
	sr.log.Debugf("registered provider for %v, requires %v", p.provides, p.requires)
	sr.providers[p.provides] = p
	return nil
}

// Request resolves a dependency tree of a given target and set up all objects on the way.
// target needs to be a pointer to a pointer to the struct that should be initialized.
// params can be used to pass additional parameter structs.
func (sr *ServiceRegistry) Request(target interface{}, params ...interface{}) error {
	// must be a pointer to a pointer
	pt := reflect.TypeOf(target)

	if pt.Kind() != reflect.Ptr {
		return fmt.Errorf("Target needs to be a pointer to a pointer but is %v", pt)
	}

	// can we provide what is requested?
	t := reflect.ValueOf(target).Elem().Type()

	// needs to be a pointer OR an interface
	// TODO: interface
	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("Dereferenced target needs to be a pointer but is %v", t)
	}

	provider, err := sr.providerFor(t)
	if err != nil {
		return err
	}

	// do we have an instance already?
	if provider.instance == nil {
		if err := sr.resolve(provider, params); err != nil {
			return err
		}
	}

	// TODO: maybe make this more functional and not set the target but let the caller to that?
	v := reflect.ValueOf(target).Elem()
	v.Set(*provider.instance)
	return nil
}

func (sr *ServiceRegistry) providerFor(t reflect.Type) (*provider, error) {
	sr.log.Debugf("requesting provider for %v", t)
	provider, found := sr.providers[t]
	if !found {
		return nil, fmt.Errorf("no provider for %v", t)
	}
	return provider, nil
}

func (sr *ServiceRegistry) resolve(p *provider, extraParams []interface{}) error {
	sr.log.Debugf("resolving %v, requires=%v", p.provides, p.requires)

	if p.instance != nil {
		return nil
	}

	if len(p.requires) == 0 {
		return sr.instantiate(p)
	}

	var params []reflect.Value
	for _, t := range p.requires {
		provider, err := sr.providerFor(t)
		if err != nil {
			return err
		}
		if err := sr.resolve(provider, extraParams); err != nil {
			return err
		}
		params = append(params, *provider.instance)
	}

	// lets attach all extra params
	for _, p := range extraParams {
		params = append(params, reflect.ValueOf(p))
	}

	return sr.instantiate(p, params...)
}

func (sr *ServiceRegistry) instantiate(p *provider, params ...reflect.Value) error {
	sr.log.Debugf("instantiate %v with %v\n", p.provides, params)

	if p.expectedParamStruct != nil {
		params = []reflect.Value{createParamStruct(p.expectedParamStruct, params)}
	}
	r := p.ctor.Call(params)
	if !r[1].IsNil() {
		return r[1].Interface().(error)
	}
	// TODO: check that this isn't nil
	v := reflect.ValueOf(r[0].Interface())
	p.instance = &v
	return nil
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
			panic("could not find struct param " + f.Type().String() + "for " + t.String())
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
