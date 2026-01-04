module github.com/NotrixInc/nx-driver-templates/driver-host-minimal

go 1.22

replace github.com/yourorg/controller-platform => ../

require google.golang.org/grpc v1.66.0

require github.com/NotrixInc/nx-driver-sdk v1.0.0

replace github.com/NotrixInc/nx-driver-sdk => ../../nx-driver-sdk
