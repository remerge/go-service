package service

import "os"

// Service Every service should provide service interface
type Service interface {
	Init() error
	Run() error
	Shutdown(sig os.Signal)
}

type BaseService struct{}

func (*BaseService) Init() error            { return nil }
func (*BaseService) Run() error             { return nil }
func (*BaseService) Shutdown(sig os.Signal) {}
