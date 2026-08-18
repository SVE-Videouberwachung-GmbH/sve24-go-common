package authz

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// DER TEST, DER DIESES PAKET BEGRUENDET.
//
// Bis zum 2026-08-18 dekodierten drei der fuenf Dienste JEDE Antwort in die
// Rechtematrix. Eine 401 traegt keinen `permissions`-Schluessel, also entstand
// eine leere Matrix — und die landete im Zwischenspeicher. Ausgeloest haette
// das kein Ausfall, sondern ein geplanter Schalter: SERVICE_TOKEN_ENFORCE.
func TestFehlerantwortWirdNichtZurLeerenMatrix(t *testing.T) {
	faelle := []struct {
		name   string
		status int
		body   string
	}{
		{"401 nach dem Scharfschalten des Dienst-Tokens", http.StatusUnauthorized, `{"error":"Service-Token ungueltig"}`},
		{"403", http.StatusForbidden, `{"error":"verboten"}`},
		{"500", http.StatusInternalServerError, `{"error":"kaputt"}`},
		{"502 vom Ingress", http.StatusBadGateway, "<html>Bad Gateway</html>"},
		{"200 mit Muell", http.StatusOK, "<html>Fehlerseite</html>"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(f.body))
			}))
			defer srv.Close()

			c := Neu(srv.URL, "geheim", "map", time.Minute)
			erg, err := c.Aufloesen(t.Context(), "max@sve24.de", nil)
			if err == nil {
				t.Fatal("kein Fehler — die Antwort waere als 'keine Rechte' durchgegangen")
			}
			if !errors.Is(err, ErrNichtPruefbar) {
				t.Errorf("err = %v, muss ErrNichtPruefbar sein — sonst kann der Aufrufer 503 nicht von 403 unterscheiden", err)
			}
			if len(erg.Rechte) != 0 {
				t.Errorf("trotz Fehler Rechte geliefert: %v", erg.Rechte)
			}
			if len(c.cache) != 0 {
				t.Errorf("Fehler im Zwischenspeicher (%d) — er wirkte die volle TTL weiter", len(c.cache))
			}
		})
	}
}

// Die gute Antwort, das App-Praefix und der Dienst-Token.
func TestAufloesenLiestDieMatrix(t *testing.T) {
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Service-Token")
		_, _ = w.Write([]byte(`{"active":true,"permissions":[
			{"module":"map.planning","level":"edit"},
			{"module":"map.team","level":"view"}]}`))
	}))
	defer srv.Close()

	erg, err := Neu(srv.URL, "geheim", "map", time.Minute).Aufloesen(t.Context(), "Max@SVE24.de", []string{"Disponent"})
	if err != nil {
		t.Fatalf("Aufloesen: %v", err)
	}
	if token != "geheim" {
		t.Errorf("Dienst-Token nicht mitgeschickt: %q", token)
	}
	// Das Praefix muss weg — der Aufrufer denkt in seinen eigenen Modulnamen.
	if erg.Rechte["planning"] != "edit" {
		t.Errorf("planning = %q, erwartet edit (Praefix map. nicht abgeschnitten?)", erg.Rechte["planning"])
	}
	if !erg.Erlaubt("planning", "view") {
		t.Error("edit muss view genuegen")
	}
	if erg.Erlaubt("team", "manage") {
		t.Error("view darf manage NICHT genuegen")
	}
	if erg.Erlaubt("archive", "view") {
		t.Error("ein Modul ohne Eintrag darf nichts erlauben")
	}
}

// Der Zwischenspeicher spart Aufrufe — aber nur fuer echte Ergebnisse.
func TestNurEchteErgebnisseWerdenGespeichert(t *testing.T) {
	var aufrufe atomic.Int32
	var fehlerhaft atomic.Bool
	fehlerhaft.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		aufrufe.Add(1)
		if fehlerhaft.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"active":true,"permissions":[{"module":"map.planning","level":"edit"}]}`))
	}))
	defer srv.Close()

	c := Neu(srv.URL, "", "map", time.Minute)
	if _, err := c.Aufloesen(t.Context(), "max@sve24.de", nil); err == nil {
		t.Fatal("Ausfall muss ein Fehler sein")
	}
	// Erholt sich id-api, muss der naechste Aufruf das SOFORT sehen.
	fehlerhaft.Store(false)
	erg, err := c.Aufloesen(t.Context(), "max@sve24.de", nil)
	if err != nil {
		t.Fatalf("nach der Erholung immer noch ein Fehler: %v", err)
	}
	if erg.Rechte["planning"] != "edit" {
		t.Fatalf("die Absage steckte im Zwischenspeicher: %v", erg.Rechte)
	}
	// Und das echte Ergebnis wird gespeichert: der dritte Aufruf fragt nicht neu.
	if _, err := c.Aufloesen(t.Context(), "max@sve24.de", nil); err != nil {
		t.Fatal(err)
	}
	if n := aufrufe.Load(); n != 2 {
		t.Errorf("%d Aufrufe, erwartet 2 — der Zwischenspeicher greift nicht", n)
	}
}
