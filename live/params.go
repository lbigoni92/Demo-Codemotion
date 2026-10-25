package live

// Lettura dei parametri di una run dalla query string. Ogni valore viene
// validato e limitato qui, lato server: il browser non è fidato.

import "strconv"

// Int legge un parametro intero con default e limiti, così il browser non può
// chiedere simulazioni assurde (es. un milione di goroutine).
func (r *Run) Int(name string, def, min, max int) int {
	v, err := strconv.Atoi(r.Params.Get(name))
	if err != nil {
		v = def
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v
}

// Bool legge un parametro booleano ("1", "true", "on").
func (r *Run) Bool(name string) bool {
	switch r.Params.Get(name) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// Str legge un parametro stringa con default. Non valida il contenuto: le demo
// lo usano solo in confronti con valori noti (es. mode == "brutal").
func (r *Run) Str(name, def string) string {
	if v := r.Params.Get(name); v != "" {
		return v
	}
	return def
}
