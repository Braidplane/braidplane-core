package model

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=Role -transform=snake -output=role_string.go -text -linecomment
type Role int

const (
	RoleUnknown    Role = iota // unknown role, should not be used
	RoleUser                   // user
	RoleSuperAdmin             // super_admin
)
