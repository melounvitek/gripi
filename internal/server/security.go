package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxRequestBodyBytes = int64(64 << 20)
	securityErrorCSP    = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
)

type nonceContextKey struct{}
type authorityContextKey struct{}

type authority struct {
	host string
	port string
}

type requestAuthorities struct {
	direct    authority
	forwarded *authority
}

type origin struct {
	scheme string
	host   string
	port   string
}

func (app *application) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		nonce, err := randomNonce()
		if err != nil {
			http.Error(response, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		policy := strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' 'nonce-" + nonce + "'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self'",
			"connect-src 'self'",
			"worker-src 'self'",
			"manifest-src 'self'",
			"object-src 'none'",
			"base-uri 'none'",
			"frame-ancestors 'none'",
			"form-action 'self'",
		}, "; ")
		response.Header().Set("Cache-Control", "private, no-store")
		response.Header().Set("Content-Security-Policy", policy)
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if app.secureTransport(request) {
			response.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), nonceContextKey{}, nonce)))
	})
}

func (app *application) limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.ContentLength > maxRequestBodyBytes {
			writeText(response, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		if request.Body == nil || request.Body == http.NoBody {
			next.ServeHTTP(response, request)
			return
		}
		if request.ContentLength >= 0 {
			request.Body = http.MaxBytesReader(response, request.Body, maxRequestBodyBytes)
			next.ServeHTTP(response, request)
			return
		}

		originalBody := request.Body
		defer originalBody.Close()
		bodyFile, err := os.CreateTemp("", "gripi-request-body-*")
		if err != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		bodyPath := bodyFile.Name()
		defer os.Remove(bodyPath)
		defer bodyFile.Close()

		written, err := io.Copy(bodyFile, io.LimitReader(originalBody, maxRequestBodyBytes+1))
		if err != nil {
			writeText(response, http.StatusBadRequest, "Invalid request body")
			return
		}
		if written > maxRequestBodyBytes {
			writeText(response, http.StatusRequestEntityTooLarge, "Request body too large")
			return
		}
		if _, err := bodyFile.Seek(0, io.SeekStart); err != nil {
			writeText(response, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		request.Body = http.MaxBytesReader(response, bodyFile, maxRequestBodyBytes)
		next.ServeHTTP(response, request)
	})
}

func (app *application) authorizeHost(next http.Handler) http.Handler {
	allowed := app.allowedHosts()
	configuredHosts := len(app.config.PermittedHosts) > 0
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		directValue := directRequestAuthority(request)
		direct, valid := parseAuthority(directValue, false)
		if !valid || !app.hostAllowed(direct.host, allowed) {
			app.blockHost(response, request, directValue, configuredHosts)
			return
		}

		var forwardedHost *authority
		if forwardedValue := lastForwardedValue(request.Header.Get("X-Forwarded-Host")); forwardedValue != "" {
			forwarded, valid := parseAuthority(forwardedValue, false)
			if !valid || !app.hostAllowed(forwarded.host, allowed) {
				app.blockHost(response, request, forwardedValue, configuredHosts)
				return
			}
			forwardedHost = &forwarded
		}
		forwardedValue, forwardedValid := rfcForwardedAuthority(request)
		if !forwardedValid {
			app.blockHost(response, request, "", configuredHosts)
			return
		}
		if forwardedValue != "" {
			forwarded, valid := parseAuthority(forwardedValue, false)
			if !valid || !app.hostAllowed(forwarded.host, allowed) {
				app.blockHost(response, request, forwardedValue, configuredHosts)
				return
			}
		}
		if app.config.TrustProxyHeaders {
			if portValue := lastForwardedValue(request.Header.Get("X-Forwarded-Port")); portValue != "" {
				port, valid := parsePort(portValue)
				if !valid {
					app.blockHost(response, request, "", configuredHosts)
					return
				}
				if forwardedHost == nil {
					forwarded := direct
					forwardedHost = &forwarded
				}
				if forwardedHost.port == "" {
					forwardedHost.port = port
				}
			}
		}

		authorities := requestAuthorities{direct: direct, forwarded: forwardedHost}
		ctx := context.WithValue(request.Context(), authorityContextKey{}, authorities)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func (app *application) allowedHosts() []string {
	bindHost := "127.0.0.1"
	if host, _, err := net.SplitHostPort(app.config.Address); err == nil {
		bindHost = host
	} else if app.config.Address != "" {
		bindHost = app.config.Address
	}
	var allowed []string
	if normalized, valid := normalizeConfiguredHost(bindHost); valid && normalized != "0.0.0.0" && normalized != "[::]" {
		allowed = append(allowed, normalized)
		if normalized == "localhost" || isLoopbackHost(normalized) {
			allowed = append(allowed, "localhost", ".localhost")
		}
	}
	for _, configured := range app.config.PermittedHosts {
		if normalized, valid := normalizeConfiguredHost(configured); valid {
			allowed = append(allowed, normalized)
		}
	}
	if !app.config.Production {
		allowed = append(allowed, "localhost", ".localhost")
	}
	return uniqueStrings(allowed)
}

func (app *application) blockHost(response http.ResponseWriter, request *http.Request, authority string, configuredHosts bool) {
	lines := []string{
		"Gripi blocked this hostname.",
		"",
		"Only continue if you recognize this as your intended Gripi address.",
	}
	parsed, valid := parseAuthority(authority, false)
	candidate := parsed.host
	if valid && candidate != "0.0.0.0" && candidate != "[::]" && !strings.HasPrefix(candidate, ".") && !strings.Contains(candidate, "*") {
		if configuredHosts {
			lines = append(lines, "Append this hostname to the existing GRIPI_PERMITTED_HOSTS value in ~/.config/gripi/env:", "", candidate)
		} else {
			lines = append(lines, "Add this line to ~/.config/gripi/env:", "", "GRIPI_PERMITTED_HOSTS="+candidate)
		}
	} else {
		lines = append(lines, "Add the intended exact hostname to GRIPI_PERMITTED_HOSTS in ~/.config/gripi/env.")
	}
	if app.localHTTPSProxyRequest(request) {
		lines = append(lines,
			"",
			"If this request intentionally comes through Tailscale Serve or another trusted HTTPS reverse proxy, also add:",
			"",
			"GRIPI_TRUST_PROXY_HEADERS=1",
			"",
			"Enable proxy trust only when clients cannot bypass a proxy that overwrites forwarded headers.",
		)
	}
	lines = append(lines,
		"",
		"Then restart Gripi. With the documented systemd service:",
		"",
		"systemctl --user restart gripi.service",
		"",
		"The request remains blocked until configuration and restart are complete.",
	)
	app.writeSecurityError(response, http.StatusForbidden, "Gateway hostname blocked", strings.Join(lines, "\n"))
}

func (app *application) localHTTPSProxyRequest(request *http.Request) bool {
	if !isLoopbackClient(request.RemoteAddr) || !strings.EqualFold(firstForwardedValue(request.Header.Get("X-Forwarded-Proto")), "https") {
		return false
	}
	direct, directValid := parseAuthority(directRequestAuthority(request), false)
	forwarded, forwardedValid := parseAuthority(firstForwardedValue(request.Header.Get("X-Forwarded-Host")), false)
	return directValid && forwardedValid && direct.host == forwarded.host
}

func (app *application) enforceSecureRemoteTransport(next http.Handler) http.Handler {
	if !app.config.Production || app.config.AllowInsecureRemoteHTTP {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if app.secureTransport(request) || (isLoopbackClient(request.RemoteAddr) && app.trustedForwardedScheme(request) == "") {
			next.ServeHTTP(response, request)
			return
		}
		writeText(response, http.StatusForbidden, "Remote Gripi access requires HTTPS. See docs/configuration.md.")
	})
}

func (app *application) secureTransport(request *http.Request) bool {
	if forwarded := app.trustedForwardedScheme(request); forwarded != "" {
		return strings.EqualFold(forwarded, "https")
	}
	return request.TLS != nil
}

func (app *application) trustedForwardedScheme(request *http.Request) string {
	if !app.config.TrustProxyHeaders {
		return ""
	}
	return firstForwardedValue(request.Header.Get("X-Forwarded-Proto"))
}

func (app *application) protectUnsafeRequestOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !unsafeMethod(request.Method) {
			next.ServeHTTP(response, request)
			return
		}
		if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") || !app.requestOriginAllowed(request) {
			title := "Cross-origin request blocked"
			message := "Gripi blocked this browser action because it could not verify that the request came from the gateway page.\n\nReturn to Gripi in a normal top-level browser tab and try again. If the gateway is behind a reverse proxy, verify that its public URL and trusted proxy settings match the documented configuration."
			if !app.config.TrustProxyHeaders && request.Header.Get("X-Forwarded-Proto") != "" {
				title = "Trusted proxy configuration required"
				message = "Gripi rejected this request because proxy headers are not trusted.\n\nIf this gateway is behind Tailscale Serve or another trusted HTTPS reverse proxy, add this line to ~/.config/gripi/env:\n\nGRIPI_TRUST_PROXY_HEADERS=1\n\nThen restart Gripi. With the documented systemd service:\n\nsystemctl --user restart gripi.service\n\nEnable this only for a trusted proxy that overwrites forwarded headers and cannot be bypassed."
			}
			app.writeSecurityError(response, http.StatusForbidden, title, message)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (app *application) requestOriginAllowed(request *http.Request) bool {
	value := strings.TrimSpace(request.Header.Get("Origin"))
	if value == "null" {
		return strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "same-origin")
	}
	if value == "" {
		value = strings.TrimSpace(request.Header.Get("Referer"))
		if value == "" {
			return true
		}
	}
	candidate, valid := normalizeOrigin(value)
	if !valid {
		return false
	}
	for _, allowed := range app.allowedRequestOrigins(request) {
		if candidate == allowed {
			return true
		}
	}
	return false
}

func (app *application) allowedRequestOrigins(request *http.Request) []origin {
	authorities, valid := request.Context().Value(authorityContextKey{}).(requestAuthorities)
	if !valid {
		return nil
	}
	var origins []origin
	if direct, valid := normalizeOrigin(requestScheme(request) + "://" + authorities.direct.String()); valid {
		origins = append(origins, direct)
	}
	if !app.config.TrustProxyHeaders {
		return origins
	}
	proto := firstForwardedValue(request.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		return origins
	}
	host := authorities.direct
	if authorities.forwarded != nil {
		host = *authorities.forwarded
	}
	if forwarded, valid := normalizeOrigin(proto + "://" + host.String()); valid {
		origins = append(origins, forwarded)
	}
	return origins
}

func (app *application) writeSecurityError(response http.ResponseWriter, status int, title, message string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", securityErrorCSP)
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	if err := app.templates.ExecuteTemplate(response, "security_error.html", struct {
		Title   string
		Message string
	}{title, message}); err != nil {
		return
	}
}

func parseForm(response http.ResponseWriter, request *http.Request) bool {
	var err error
	mediaType, _, mediaTypeErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if mediaTypeErr == nil && mediaType == "multipart/form-data" {
		err = request.ParseMultipartForm(0)
	} else {
		err = request.ParseForm()
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeText(response, http.StatusRequestEntityTooLarge, "Request body too large")
	} else {
		writeText(response, http.StatusBadRequest, "Invalid request body")
	}
	return false
}

func randomNonce() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(value), nil
}

func requestNonce(request *http.Request) string {
	nonce, _ := request.Context().Value(nonceContextKey{}).(string)
	return nonce
}

func unsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func requestScheme(request *http.Request) string {
	if request.TLS != nil {
		return "https"
	}
	return "http"
}

func directRequestAuthority(request *http.Request) string {
	if request.Host != "" {
		return request.Host
	}
	return request.URL.Host
}

func normalizeOrigin(value string) (origin, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return origin{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return origin{}, false
	}
	authority, valid := parseAuthority(parsed.Host, false)
	if !valid {
		return origin{}, false
	}
	port := authority.port
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return origin{scheme: scheme, host: authority.host, port: port}, true
}

func normalizeConfiguredHost(value string) (string, bool) {
	authority, valid := parseAuthority(strings.TrimSpace(value), true)
	return authority.host, valid
}

func parseAuthority(value string, allowPatterns bool) (authority, bool) {
	if value == "" || strings.IndexFunc(value, func(character rune) bool {
		return character < ' ' || character == '\u007f'
	}) >= 0 {
		return authority{}, false
	}

	hostValue := value
	port := ""
	if strings.HasPrefix(value, "[") {
		closing := strings.IndexByte(value, ']')
		if closing < 0 {
			return authority{}, false
		}
		hostValue = value[1:closing]
		remainder := value[closing+1:]
		if remainder != "" {
			if !strings.HasPrefix(remainder, ":") {
				return authority{}, false
			}
			var valid bool
			port, valid = parsePort(remainder[1:])
			if !valid {
				return authority{}, false
			}
		}
		address, err := netip.ParseAddr(hostValue)
		if err != nil || !address.Is6() || address.Zone() != "" {
			return authority{}, false
		}
		address = address.Unmap()
		if address.Is4() {
			return authority{host: address.String(), port: port}, true
		}
		return authority{host: "[" + address.String() + "]", port: port}, true
	}
	if strings.ContainsAny(value, "[]") {
		return authority{}, false
	}

	colonCount := strings.Count(value, ":")
	if colonCount > 1 {
		if allowPatterns {
			if address, err := netip.ParseAddr(value); err == nil && address.Is6() && address.Zone() == "" {
				return authority{host: "[" + address.String() + "]"}, true
			}
		}
		return authority{}, false
	}
	if colonCount == 1 {
		hostValue, value, _ = strings.Cut(value, ":")
		var valid bool
		port, valid = parsePort(value)
		if !valid {
			return authority{}, false
		}
	}

	host, valid := normalizeAuthorityHost(hostValue, allowPatterns)
	if !valid {
		return authority{}, false
	}
	return authority{host: host, port: port}, true
}

func normalizeAuthorityHost(value string, allowPatterns bool) (string, bool) {
	if address, err := netip.ParseAddr(value); err == nil && address.Zone() == "" {
		address = address.Unmap()
		if address.Is6() {
			return "", false
		}
		return address.String(), true
	}
	value = strings.ToLower(value)
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune(".-_", character) || allowPatterns && character == '*' {
			continue
		}
		return "", false
	}
	if value == "" || value == "." || strings.Contains(value, "..") || !allowPatterns && strings.HasPrefix(value, ".") {
		return "", false
	}
	return value, true
}

func parsePort(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return "", false
	}
	return strconv.FormatUint(port, 10), true
}

func (value authority) String() string {
	if value.port == "" {
		return value.host
	}
	return value.host + ":" + value.port
}

func (app *application) hostAllowed(host string, allowed []string) bool {
	if !app.config.Production && (isIPAddress(host) || host == "test" || strings.HasSuffix(host, ".test")) {
		return true
	}
	for _, candidate := range allowed {
		if candidate == host {
			return true
		}
		if strings.HasPrefix(candidate, ".") && (host == strings.TrimPrefix(candidate, ".") || strings.HasSuffix(host, candidate)) {
			return true
		}
	}
	return false
}

func rfcForwardedAuthority(request *http.Request) (string, bool) {
	forwardedHost := ""
	for _, headerValue := range request.Header.Values("Forwarded") {
		elements, valid := splitForwardedValue(headerValue, ',')
		if !valid {
			return "", false
		}
		for _, element := range elements {
			if strings.TrimSpace(element) == "" {
				return "", false
			}
			parts, valid := splitForwardedValue(element, ';')
			if !valid {
				return "", false
			}
			seen := make(map[string]struct{}, len(parts))
			for _, part := range parts {
				key, value, found := strings.Cut(strings.TrimSpace(part), "=")
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)
				if !found || !validForwardedToken(key) || value == "" {
					return "", false
				}
				normalizedKey := strings.ToLower(key)
				if _, duplicate := seen[normalizedKey]; duplicate {
					return "", false
				}
				seen[normalizedKey] = struct{}{}
				if value[0] == '"' {
					value, valid = unquoteForwardedValue(value)
				} else {
					valid = validForwardedToken(value)
				}
				if !valid || value == "" {
					return "", false
				}
				if normalizedKey == "host" {
					forwardedHost = value
				}
			}
		}
	}
	return forwardedHost, true
}

func validForwardedToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character >= '\u007f' || strings.ContainsRune(`()<>@,;:\"/[]?={}`, character) {
			return false
		}
	}
	return true
}

func unquoteForwardedValue(value string) (string, bool) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	unquoted := make([]byte, 0, len(value)-2)
	for index := 1; index < len(value)-1; index++ {
		if value[index] == '"' {
			return "", false
		}
		if value[index] == '\\' {
			index++
			if index >= len(value)-1 {
				return "", false
			}
		}
		unquoted = append(unquoted, value[index])
	}
	return string(unquoted), true
}

func splitForwardedValue(value string, separator byte) ([]string, bool) {
	parts := make([]string, 0, strings.Count(value, string(separator))+1)
	start := 0
	quoted := false
	escaped := false
	for index := 0; index < len(value); index++ {
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if value[index] == '\\' {
				escaped = true
				continue
			}
			if value[index] == '"' {
				quoted = false
			}
			continue
		}
		if value[index] == '"' {
			quoted = true
			continue
		}
		if value[index] == separator {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	if quoted {
		return nil, false
	}
	return append(parts, value[start:]), true
}

func isIPAddress(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	_, err := netip.ParseAddr(host)
	return err == nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func isLoopbackClient(remoteAddress string) bool {
	ip := clientIP(remoteAddress)
	return ip != "" && isLoopbackHost(ip)
}

func clientIP(remoteAddress string) string {
	if host, _, err := net.SplitHostPort(remoteAddress); err == nil {
		return host
	}
	if address, err := netip.ParseAddr(remoteAddress); err == nil {
		return address.String()
	}
	return ""
}

func firstForwardedValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}

func lastForwardedValue(value string) string {
	last := ""
	for part := range strings.SplitSeq(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			last = part
		}
	}
	return last
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func writeText(response http.ResponseWriter, status int, body string) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(status)
	_, _ = response.Write([]byte(body))
}

func absoluteRedirectURL(request *http.Request, target string, trustProxy bool) string {
	authorities, valid := request.Context().Value(authorityContextKey{}).(requestAuthorities)
	if !valid {
		return target
	}
	scheme := requestScheme(request)
	authority := authorities.direct
	if trustProxy {
		if forwarded := strings.ToLower(firstForwardedValue(request.Header.Get("X-Forwarded-Proto"))); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
		if authorities.forwarded != nil {
			authority = *authorities.forwarded
		}
	}
	return scheme + "://" + authority.String() + target
}
