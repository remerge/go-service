# go-service

Package `service` provides an opinionated base class for implementing services
(daemons) in Go.

## Install

```bash
go get github.com/remerge/go-service
```

## Usage
* Every service should
1. Implement service interface
2. Initialise service executor.
3. You can add tracker or server to service executor (or both)

* Executor can be used in an implicit composition
* You can check out very simple example [in main folder](main/service.go)
* Or example [with server](main/with_server.go)
