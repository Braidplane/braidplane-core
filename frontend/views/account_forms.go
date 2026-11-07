package views

import (
	"context"

	"github.com/a-h/templ"
)

type AccountForms struct {
	Notice  templ.Component
	Fields  templ.Component
	Enabled bool
}

type accountFormsKey struct{}

func WithAccountForms(ctx context.Context, forms AccountForms) context.Context {
	return context.WithValue(ctx, accountFormsKey{}, forms)
}

func AccountNotice(ctx context.Context) templ.Component {
	forms, _ := ctx.Value(accountFormsKey{}).(AccountForms)
	if forms.Notice == nil {
		return templ.NopComponent
	}
	return forms.Notice
}

func AccountFields(ctx context.Context) templ.Component {
	forms, _ := ctx.Value(accountFormsKey{}).(AccountForms)
	if forms.Fields == nil {
		return templ.NopComponent
	}
	return forms.Fields
}

func AccountActionsEnabled(ctx context.Context) bool {
	forms, ok := ctx.Value(accountFormsKey{}).(AccountForms)
	return !ok || forms.Enabled
}

func HasAccountNotice(ctx context.Context) bool {
	forms, _ := ctx.Value(accountFormsKey{}).(AccountForms)
	return forms.Notice != nil
}
