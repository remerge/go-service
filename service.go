package service

import "os"

// Service defines an interface who's implementors are executable by the Exceutor.
type Service interface {
	Init() error
	Run() error
	Shutdown(sig os.Signal)
}

type BaseService struct{}

func (*BaseService) Init() error            { return nil }
func (*BaseService) Run() error             { return nil }
func (*BaseService) Shutdown(sig os.Signal) {}

// Registry is the interface to our service registry, this
// should be defined in every package relying on the registry to decouple
// it from the actual registry.Registry interface
type Registry interface{ Register(ctor interface{}) error }
