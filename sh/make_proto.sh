#!/bin/bash
protoc --go_out=. --go-grpc_out=. ./goorm/rpc/v1/goorm_v1.proto