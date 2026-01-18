module github.com/NotrixInc/nx-driver-templates/driver-template-child-go

go 1.22

require google.golang.org/grpc v1.66.0 // indirect

require github.com/NotrixInc/nx-driver-sdk v1.0.0

require (
	golang.org/x/net v0.26.0 // indirect
	golang.org/x/sys v0.21.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240604185151-ef581f913117 // indirect
	google.golang.org/protobuf v1.34.1 // indirect
)

replace github.com/NotrixInc/nx-driver-sdk => ../../nx-driver-sdk
