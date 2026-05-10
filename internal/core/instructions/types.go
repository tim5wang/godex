package instructions

// InstructionSource describes one loaded instruction file and its precedence.
type InstructionSource struct {
	Path     string
	Scope    string
	Priority int
	Content  string
}
