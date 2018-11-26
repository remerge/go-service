package service

import "os"

// Service does not need a run method anymore as there is no usecase that can't be handled in init
// Init has to do some initialization and needs  to return. Functionality that needs to run in the background should be scheduled as go routines
type Service interface {
	Init() error
	Shutdown(sig os.Signal)
}

// Registry is the interface to our Component registry, this
// should be defined in every package relying on the registry to decouple
// it from the actual registry.Registry interface
type Registry interface {
	Register(ctor interface{}) (func(...interface{}) (interface{}, error), error)
}
