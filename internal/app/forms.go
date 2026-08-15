package app

// Validator accumulates field errors; embed it in every form struct.
type Validator struct {
	FieldErrors map[string]string
}

// Valid reports whether every Check passed.
func (v *Validator) Valid() bool { return len(v.FieldErrors) == 0 }

// Check records msg under field when ok is false. The first failed check per
// field wins — order checks from most to least fundamental.
func (v *Validator) Check(ok bool, field, msg string) {
	if ok {
		return
	}
	if v.FieldErrors == nil {
		v.FieldErrors = make(map[string]string)
	}
	if _, taken := v.FieldErrors[field]; !taken {
		v.FieldErrors[field] = msg
	}
}

// taskForm is the one form in this app: the submitted title and its errors. An
// invalid submission renders it back with both, so nothing is retyped.
type taskForm struct {
	Title string
	Validator
}
