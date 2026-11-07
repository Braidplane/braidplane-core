package model

type Permission struct {
	Resource string
	Action   string
}

type RolePermission struct {
	Resource string
	Action   string
}
