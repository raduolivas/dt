module github.com/raduolivas/dt/backend/tools/dtctl

go 1.27.3

require (
	connectrpc.com/connect/v2 v2.0.1
	github.com/raduolivas/dt/backend/gen v0.0.0
	github.com/raduolivas/dt/backend/pkg v0.0.0
	google.golang.org/protobuf v1.36.12
)

replace github.com/raduolivas/dt/backend/gen => ../../gen

replace github.com/raduolivas/dt/backend/pkg => ../../pkg
