package service

import "os"

// Service Every service should provide service interface
type Service interface {
	Init() error
	Run() error
	Shutdown(os.Signal)
}
