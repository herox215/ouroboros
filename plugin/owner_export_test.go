package plugin

// WithBinary makes o start exe as the node, for tests whose binary is ouro
// only when it is called with a command.
func (o *Owner) WithBinary(exe func() (string, error)) *Owner {
	o.local.Exe = exe
	return o
}
