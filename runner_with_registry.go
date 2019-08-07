package service

import (
	"reflect"

	"github.com/remerge/go-service/registry"
)

func NewRunnerWithRegistry() *RunnerWithRegistry {
	return &RunnerWithRegistry{
		Registry: registry.New(),
		Runner:   NewRunner(),
	}
}

type RunnerWithRegistry struct {
	*registry.Registry
	*Runner
}

// Create creates an instance and sets s (which must be a pointer) to the new instance given
// a set of parameters. Furthermore it adds the new object to the list of executed services.
func (r *RunnerWithRegistry) Create(s interface{}, params ...interface{}) {
	err := r.RequestAndSet(s, params...)
	if err != nil {
		panic(err)
	}
	// s is a pointer to a pointer to a type instance implementing a Service
	// TODO: how to cast this without reflections? Am I stupid?
	sv := reflect.ValueOf(s).Elem().Interface().(Service)
	r.Add(sv)

}

func (r *RunnerWithRegistry) CreateOrdered(services ...interface{}) {
	for _, s := range services {
		r.Create(s)
	}
}
