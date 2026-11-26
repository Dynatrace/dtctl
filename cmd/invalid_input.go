package cmd

// invalidInputError marks an error as a problem with the value a caller
// passed, so agent mode reports it as validation_error and the command exits
// with the usage code. Without it the code is
// guessed from the message text, and the same kind of mistake gets different
// codes depending on its wording: a path such as @config.json would read as a
// config_error.
type invalidInputError struct {
	err error
}

func (e *invalidInputError) Error() string { return e.err.Error() }
func (e *invalidInputError) Unwrap() error { return e.err }
