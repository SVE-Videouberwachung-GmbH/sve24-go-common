// Package authz spricht mit id-api ueber die Frage "was darf diese Person in
// dieser Anwendung".
//
// WARUM ES DIESES PAKET GIBT. Bis zum 2026-08-18 hatte jeder der fuenf Dienste
// seinen eigenen Client — funktional dasselbe, in Details verschieden. Drei von
// fuenf dekodierten JEDE Antwort in die Rechtematrix, auch eine Fehlerantwort:
// eine 401 traegt keinen `permissions`-Schluessel, also entstand eine LEERE
// Matrix, und die landete im Zwischenspeicher. Beim Scharfschalten des
// Dienst-Tokens haette das jedem Benutzer 403 gegeben, ohne Log, ohne Meldung.
//
// Der Fehler konnte nur entstehen, weil es den Code fuenfmal gab. Deshalb steht
// er jetzt einmal hier — samt der Tests, die genau diesen Fall festhalten.
package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNichtPruefbar heisst: die Identitaet steht fest, aber id-api konnte nicht
// sagen, was sie darf.
//
// Das ist ausdruecklich NICHT dasselbe wie "keine Rechte". Der Aufrufer
// antwortet darauf mit 503 und einer Log-Zeile — nicht mit 403, und erst recht
// nicht mit 401: Letzteres schickt jemanden in eine Anmeldung, die er laengst
// hinter sich hat.
var ErrNichtPruefbar = errors.New("Rechte nicht pruefbar")

// Ergebnis ist die aufgeloeste Matrix fuer eine Identitaet.
//
// Die Modulschluessel sind OHNE App-Praefix: id-api liefert "map.planning",
// hier steht "planning". So bleibt der Aufrufer bei den Namen, die er selbst
// benutzt.
type Ergebnis struct {
	Aktiv  bool
	Rechte map[string]string
}

var rang = map[string]int{"": 0, "none": 0, "view": 1, "edit": 2, "manage": 3}

// Erlaubt meldet, ob die Stufe fuer dieses Modul mindestens `stufe` ist.
func (e Ergebnis) Erlaubt(modul, stufe string) bool {
	return rang[e.Rechte[modul]] >= rang[stufe]
}

type eintrag struct {
	erg    Ergebnis
	ablauf time.Time
}

// Client fragt die Rechtematrix bei id-api ab, mit kurzem Zwischenspeicher.
type Client struct {
	basisURL string
	token    string
	app      string
	http     *http.Client
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]eintrag
}

// Option passt den Client an, ohne die Signatur wachsen zu lassen.
type Option func(*Client)

// MitHTTPClient ersetzt den voreingestellten Client — dafuer gedacht, ihn mit
// otelhttp zu umhuellen. BEWUSST NICHT FEST VERDRAHTET: sonst zwingt dieses
// Paket jedem Dienst die OpenTelemetry-Abhaengigkeit auf, auch dem, der sie
// nicht benutzt.
func MitHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// Neu baut einen Client gegen id-api.
//
// `app` ist der Schluessel dieser Anwendung in der Rechtematrix ("map", "prod",
// "wol", …). Ein leerer Token bedeutet: keine Kopfzeile — id-api protokolliert
// das, solange es den Token noch nicht verlangt.
func Neu(basisURL, token, app string, ttl time.Duration, opts ...Option) *Client {
	c := &Client{
		basisURL: strings.TrimRight(basisURL, "/"),
		token:    token,
		app:      app,
		ttl:      ttl,
		http:     &http.Client{Timeout: 5 * time.Second},
		cache:    map[string]eintrag{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Aufloesen liefert die Rechte der Identitaet, zwischengespeichert.
//
// JEDER FEHLERWEG IST EIN FEHLER, nie eine leere Matrix — siehe Paketkommentar.
// Und ein Fehler wird NICHT zwischengespeichert: sonst wirkt er die volle TTL
// weiter, auch wenn id-api laengst wieder antwortet. Das ist der Unterschied
// zwischen "Token korrigiert, laeuft wieder" und "Token korrigiert, und es
// bleibt trotzdem kaputt".
func (c *Client) Aufloesen(ctx context.Context, email string, gruppen []string) (Ergebnis, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	schluessel := email + "|" + strings.Join(gruppen, ",")

	c.mu.Lock()
	if e, ok := c.cache[schluessel]; ok && time.Now().Before(e.ablauf) {
		c.mu.Unlock()
		return e.erg, nil
	}
	c.mu.Unlock()

	koerper, err := json.Marshal(map[string]any{"email": email, "app": c.app, "groups": gruppen})
	if err != nil {
		return Ergebnis{}, fmt.Errorf("%w: Anfrage bauen: %w", ErrNichtPruefbar, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.basisURL+"/api/authz/resolve", bytes.NewReader(koerper))
	if err != nil {
		return Ergebnis{}, fmt.Errorf("%w: Anfrage bauen: %w", ErrNichtPruefbar, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-Service-Token", c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Ergebnis{}, fmt.Errorf("%w: id-api nicht erreichbar: %w", ErrNichtPruefbar, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Ergebnis{}, fmt.Errorf("%w: id-api antwortete mit %d", ErrNichtPruefbar, resp.StatusCode)
	}

	var roh struct {
		Active      bool `json:"active"`
		Permissions []struct {
			Module string `json:"module"`
			Level  string `json:"level"`
		} `json:"permissions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&roh); err != nil {
		return Ergebnis{}, fmt.Errorf("%w: Antwort lesen: %w", ErrNichtPruefbar, err)
	}

	erg := Ergebnis{Aktiv: roh.Active, Rechte: map[string]string{}}
	for _, p := range roh.Permissions {
		erg.Rechte[strings.TrimPrefix(p.Module, c.app+".")] = p.Level
	}

	c.mu.Lock()
	c.cache[schluessel] = eintrag{erg: erg, ablauf: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return erg, nil
}
