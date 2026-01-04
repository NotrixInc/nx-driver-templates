module github.com/NotrixInc/nx-driver-templates/driver-template-child-go

go 1.22

require google.golang.org/grpc v1.66.0

require github.com/NotrixInc/nx-driver-sdk v1.0.0

replace github.com/NotrixInc/nx-driver-sdk => ../../nx-driver-sdk
