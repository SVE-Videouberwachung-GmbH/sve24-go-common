# CLAUDE.md — sve24-go-common

Geteilte Go-Bausteine der SVE-Dienste. **Kein Dienst** — dieses Repo wird nie
ausgerollt, es wird importiert.

## Warum es das gibt

Bis zum 2026-08-18 hatte jeder der fünf Go-Dienste seinen eigenen Client für
`id-api → POST /api/authz/resolve`. Funktional dasselbe, in Details verschieden
— und **drei von fünf trugen denselben Fehler**: sie dekodierten *jede* Antwort
in die Rechtematrix, auch eine Fehlerantwort. Eine 401 trägt keinen
`permissions`-Schlüssel, also entstand eine leere Matrix, und die landete im
Zwischenspeicher. Beim Scharfschalten des Dienst-Tokens hätte das jedem
Benutzer 403 gegeben — ohne Log, ohne Meldung.

**Der Fehler konnte nur entstehen, weil es den Code fünfmal gab.** Genau dafür
ist dieses Repo da: was alle Dienste gleich machen müssen, steht einmal hier.

## Was hier hineingehört — und was nicht

**Ja:** Bausteine, bei denen ein Unterschied zwischen den Diensten ein *Fehler*
wäre. Rechteauflösung, Identitäts-Header vom Edge, Dienst-Token.

**Nein:** alles Fachliche. Ein geteiltes Modul, in das „praktisches" wandert,
wird zur Sammelstelle, und dann hängt jeder Dienst an jeder Änderung. Im
Zweifel: nicht hier.

**Faustregel:** Zwei Dienste, die dasselbe brauchen, dürfen es doppelt haben.
Beim dritten wird es hierher gezogen (Rule of Three, Wurzel-`CLAUDE.md` §3).

## Pakete

| Paket | Zweck |
|---|---|
| `authz` | Rechtematrix bei id-api abfragen, mit kurzem Zwischenspeicher |

## Regeln für dieses Repo

- **Keine Abhängigkeit, die ein Dienst nicht schon hat.** Der HTTP-Client ist
  deshalb über `MitHTTPClient` austauschbar, statt OpenTelemetry fest zu
  verdrahten — wer Traces will, reicht seinen umhüllten Client herein.
- **Jede Änderung ist eine Änderung an fünf Diensten.** Breaking Changes
  brauchen einen guten Grund und eine neue Hauptversion (SemVer).
- **Fehler sind Fehler, nie leere Ergebnisse.** Das ist die Lehre, die dieses
  Repo begründet hat; sie steht als Test in `authz/client_test.go` und darf
  nicht aufgeweicht werden.
- `make ci` fährt exakt das, was der Runner fährt.
