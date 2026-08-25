package niri

// refusalError marks a complete niri reply that says it did not perform the
// request. Callers can distinguish that from a lost reply after a write.
type refusalError string

func (failure refusalError) Error() string { return string(failure) }

func (refusalError) Refused() bool { return true }

func refused(message string) error { return refusalError(message) }
