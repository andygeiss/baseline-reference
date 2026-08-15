package app

// Validator accumulates errors; embed it in every form struct.
type Validator struct {
	FieldErrors map[string]string

	// FormError is the failure that belongs to no single field. Login is the
	// reason it exists: "we don't know that name and password" cannot be
	// attached to either box, because saying which of the two was wrong is
	// exactly what tells an attacker which names exist.
	FormError string
}

// Valid reports whether every Check passed.
func (v *Validator) Valid() bool { return len(v.FieldErrors) == 0 && v.FormError == "" }

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

// CheckForm records msg against the whole form when ok is false.
func (v *Validator) CheckForm(ok bool, msg string) {
	if !ok && v.FormError == "" {
		v.FormError = msg
	}
}

// registerForm keeps what a failed registration re-renders. The password is not
// a field here: a re-rendered password is a password written into the HTML, the
// browser's cache, and any proxy that sees the page.
type registerForm struct {
	Name   string
	Invite string
	Validator
}

// loginForm keeps only the name, for the same reason.
type loginForm struct {
	Name string
	Validator
}

type roomForm struct {
	Name string
	Validator
}

type messageForm struct {
	Body string
	Validator
}

type tokenForm struct {
	Label string
	Validator
}
