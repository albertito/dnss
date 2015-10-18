package dnss

// Generate the protobuf+grpc service.
//go:generate protoc --go_out=plugins=grpc:. dnss.proto
