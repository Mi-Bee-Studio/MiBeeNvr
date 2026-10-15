package dlna

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// ServerConfig is the resolved runtime config for the DLNA server.
type ServerConfig struct {
	Port         int      // HTTP description/control port; 0 = default 49152
	Interfaces   []string // interface names or empty = auto (LAN IPv4)
	FriendlyName string   // shown on renderers; empty = "MiBee NVR"
}

// Server runs the DLNA media server: per-interface SSDP announcements plus
// one HTTP listener per interface serving the device description, CDS/CM
// SOAP control, and the recording items themselves. Implements the
// pkg/app optional-service lifecycle (Start blocks, Stop/Close unwinds).
type Server struct {
	cfg     ServerConfig
	content *contentDirectory
	udn     string
	server  string // SSDP SERVER header value

	mu        sync.Mutex
	closed    bool
	conn      *net.UDPConn
	pc        *ipv4.PacketConn
	ifaces    []*net.Interface
	listeners []net.Listener
	stopped   chan struct{}
	stopOnce  sync.Once
}

// NewServer builds a DLNA server. machineKey feeds the stable UDN (hostname
// + storage root at the call site).
func NewServer(cfg ServerConfig, db *storage.DB, store *storage.Manager, machineKey string) *Server {
	if cfg.Port == 0 {
		cfg.Port = 49152
	}
	if strings.TrimSpace(cfg.FriendlyName) == "" {
		cfg.FriendlyName = "MiBee NVR"
	}
	host, _ := os.Hostname()
	return &Server{
		cfg:     cfg,
		content: &contentDirectory{db: db, store: store},
		udn:     UDN(machineKey),
		server:  "MiBeeNVR/1.0 UPnP/1.0 DLNADOC/1.50 " + host + "/1.0",
		stopped: make(chan struct{}),
	}
}

// Handler returns the HTTP surface (device description, SCPDs, SOAP control,
// GENA stubs, item serving) — split from Start so tests can drive it with
// httptest without any sockets.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/dlna/device.xml", func(w http.ResponseWriter, r *http.Request) {
		// The description embeds a per-request URLBase from the Host header
		// so service URLs resolve on the interface the renderer used.
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "text/xml; charset=\"utf-8\"")
		w.Write(deviceDescription(s.cfg.FriendlyName, s.udn, base))
	})
	mux.HandleFunc("/dlna/ContentDirectory/scpd.xml", serveXML(scpdContentDirectory()))
	mux.HandleFunc("/dlna/ConnectionManager/scpd.xml", serveXML(scpdConnectionManager()))
	mux.HandleFunc("/dlna/ContentDirectory/control", s.handleContentDirectoryControl)
	mux.HandleFunc("/dlna/ConnectionManager/control", s.handleConnectionManagerControl)
	mux.HandleFunc("/dlna/ContentDirectory/event", s.genaStub)
	mux.HandleFunc("/dlna/ConnectionManager/event", s.genaStub)
	mux.HandleFunc("/dlna/item/", s.handleItem)
	return mux
}

func serveXML(doc []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=\"utf-8\"")
		w.Write(doc)
	}
}

// handleContentDirectoryControl dispatches CDS SOAP actions. Only Browse is
// implemented; the SCPD advertises nothing else.
func (s *Server) handleContentDirectoryControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	buf, err := io.ReadAll(body)
	if err != nil {
		writeSOAP(w, http.StatusBadRequest, soapFault(errInvalidAction, "unreadable body"))
		return
	}
	if soapActionName(r.Header.Get("SOAPAction")) != "Browse" {
		writeSOAP(w, http.StatusBadRequest, soapFault(errInvalidAction, "only Browse is implemented"))
		return
	}
	args, err := parseBrowse(buf)
	if err != nil {
		writeSOAP(w, http.StatusBadRequest, soapFault(errInvalidAction, err.Error()))
		return
	}
	base := "http://" + r.Host
	res, err := s.content.browse(r.Context(), args.ObjectID, args.BrowseFlag, base)
	if err != nil {
		if errors.Is(err, errNoSuchObject) {
			writeSOAP(w, soapStatusNotFound, soapFault(errNoSuchObjectC, "no such object"))
			return
		}
		writeSOAP(w, http.StatusInternalServerError, soapFault(501, "action failed: "+err.Error()))
		return
	}
	frags := res.frags
	from, to := paginate(len(frags), args.StartingIndex, args.RequestedCount)
	page := frags[from:to]
	inner := "<Result>" + xmlEscape(didlDocument(page)) + "</Result>" +
		"<NumberReturned>" + strconv.Itoa(len(page)) + "</NumberReturned>" +
		"<TotalMatches>" + strconv.Itoa(res.totalMatches) + "</TotalMatches>" +
		"<UpdateID>1</UpdateID>"
	writeSOAP(w, http.StatusOK, soapResponse("urn:schemas-upnp-org:service:ContentDirectory:1", "Browse", inner))
}

// soapStatusNotFound: UPnP control errors travel in a 500 envelope normally,
// but 701 no-such-object is conventionally 404-ish; keep 500 per spec.
const soapStatusNotFound = http.StatusInternalServerError

func (s *Server) handleConnectionManagerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	switch soapActionName(r.Header.Get("SOAPAction")) {
	case "GetProtocolInfo":
		inner := "<Source></Source><Sink></Sink>"
		writeSOAP(w, http.StatusOK, soapResponse("urn:schemas-upnp-org:service:ConnectionManager:1", "GetProtocolInfo", inner))
	case "GetCurrentConnectionIDs":
		inner := "<ConnectionIDs></ConnectionIDs>"
		writeSOAP(w, http.StatusOK, soapResponse("urn:schemas-upnp-org:service:ConnectionManager:1", "GetCurrentConnectionIDs", inner))
	default:
		writeSOAP(w, http.StatusBadRequest, soapFault(errInvalidAction, "unsupported action"))
	}
}

// genaStub answers GENA SUBSCRIBE/UNSUBSCRIBE without emitting events —
// renderers treat silence as "no changes" and re-Browse. Some stacks refuse
// to list a server whose eventSubURL 404s, so the endpoint exists.
func (s *Server) genaStub(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "SUBSCRIBE", "UNSUBSCRIBE":
		if r.Method == "SUBSCRIBE" {
			w.Header().Set("SID", s.udn)
			w.Header().Set("TIMEOUT", "Second-1800")
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "SUBSCRIBE, UNSUBSCRIBE")
		http.Error(w, "GENA only", http.StatusMethodNotAllowed)
	}
}

// handleItem serves a recording file with Range support (http.ServeContent)
// over the DLNA listener — LAN-side, no API auth involved by design.
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET/HEAD only", http.StatusMethodNotAllowed)
		return
	}
	recID := strings.TrimPrefix(r.URL.Path, "/dlna/item/")
	if recID == "" || strings.Contains(recID, "/") {
		http.NotFound(w, r)
		return
	}
	rec, err := s.content.db.GetRecording(r.Context(), recID)
	if err != nil || rec == nil {
		http.NotFound(w, r)
		return
	}
	path, err := s.content.recordingPath(rec)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	mime, _ := mimeForFormat(string(rec.Format))
	w.Header().Set("Content-Type", mime)
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Name drives extension-based fallback sniffing; Content-Type is already
	// set explicitly so DLNA renderers see the advertised mime verbatim.
	http.ServeContent(w, r, rec.ID+filepath.Ext(path), st.ModTime(), f)
}

func writeSOAP(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "text/xml; charset=\"utf-8\"")
	w.Header().Set("Ext", "")
	w.WriteHeader(status)
	w.Write(body)
}

// Start binds the SSDP multicast socket and per-interface HTTP listeners,
// then blocks until ctx is cancelled (or Stop/Close is called). A bind
// failure returns an error — registration treats DLNA as best-effort and
// logs, never aborts startup.
func (s *Server) Start(ctx context.Context) error {
	ifaces, err := ssdpInterfaces(s.cfg.Interfaces)
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		return fmt.Errorf("dlna: no multicast-capable LAN interfaces found")
	}

	raw, err := ssdpListenConfig.ListenPacket(ctx, "udp4", "0.0.0.0:1900")
	if err != nil {
		return fmt.Errorf("dlna: ssdp listen :1900: %w", err)
	}
	conn := raw.(*net.UDPConn)
	pc := ipv4.NewPacketConn(conn)
	joined := 0
	for _, ifi := range ifaces {
		if err := pc.JoinGroup(ifi, &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250)}); err == nil {
			joined++
		} else {
			slog.Warn("dlna: multicast join failed", "interface", ifi.Name, "error", err)
		}
	}
	if joined == 0 {
		conn.Close()
		return fmt.Errorf("dlna: could not join SSDP multicast group on any interface")
	}

	s.mu.Lock()
	s.conn = conn
	s.pc = pc
	s.ifaces = ifaces
	s.mu.Unlock()

	// One HTTP listener per interface IP so LOCATION matches the network the
	// renderer discovered us on.
	handler := s.Handler()
	type bound struct {
		l  net.Listener
		ip net.IP
	}
	var bounds []bound
	for _, ifi := range ifaces {
		ip := ipv4Addr(*ifi)
		if ip == nil {
			continue
		}
		l, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(s.cfg.Port)))
		if err != nil {
			for _, b := range bounds {
				b.l.Close()
			}
			conn.Close()
			return fmt.Errorf("dlna: http listen %s: %w", ip, err)
		}
		bounds = append(bounds, bound{l: l, ip: ip})
		s.mu.Lock()
		s.listeners = append(s.listeners, l)
		s.mu.Unlock()
		go func() {
			srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
			if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
				slog.Warn("dlna: http serve stopped", "error", err)
			}
		}()
	}

	s.announceAll() // initial alive burst

	go s.readLoop()
	go s.aliveLoop(ctx)

	<-ctx.Done()
	s.Stop()
	<-s.stopped
	return nil
}

// readLoop answers M-SEARCHes with one unicast response per matching target.
func (s *Server) readLoop() {
	buf := make([]byte, 4096)
	for {
		s.mu.Lock()
		conn, pc := s.conn, s.pc
		s.mu.Unlock()
		if conn == nil || pc == nil {
			return
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		st, ok := parseMSearch(buf[:n])
		if !ok {
			continue
		}
		s.mu.Lock()
		ifaces := s.ifaces
		s.mu.Unlock()
		location := s.locationFor(ifaces, src)
		if location == "" {
			continue
		}
		for _, pair := range ssdpSearchTargets(s.udn) {
			if st == "ssdp:all" || st == pair[0] {
				_, _ = conn.WriteToUDP(buildMSearchResponse(pair[0], pair[1], location, s.server), src)
			}
		}
	}
}

// locationFor picks the advertised URL base for a reply: the interface whose
// subnet contains the requester, else the first interface.
func (s *Server) locationFor(ifaces []*net.Interface, src *net.UDPAddr) string {
	var first string
	for _, ifi := range ifaces {
		ip := ipv4Addr(*ifi)
		if ip == nil {
			continue
		}
		if first == "" {
			first = fmt.Sprintf("http://%s:%d/dlna/device.xml", ip, s.cfg.Port)
		}
		if src != nil && src.IP != nil && subnetContains(ifi, src.IP) {
			return fmt.Sprintf("http://%s:%d/dlna/device.xml", ip, s.cfg.Port)
		}
	}
	return first
}

// subnetContains reports whether src falls inside one of the interface's
// IPv4 subnets (mask applied to both ends).
func subnetContains(ifi *net.Interface, src net.IP) bool {
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		mask := ipnet.Mask
		if len(mask) == 16 { // IPv6-mapped form
			mask = mask[12:]
		}
		if src.To4() != nil && ip4.Mask(mask).Equal(src.To4().Mask(mask)) {
			return true
		}
	}
	return false
}

// aliveLoop re-announces at ~1/3 of max-age until the server stops.
func (s *Server) aliveLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(ssdpMaxAge/3) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopped:
			return
		case <-t.C:
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			s.announceAll()
		}
	}
}

// announceAll sends ssdp:alive for every target on every interface.
func (s *Server) announceAll() {
	s.sendAll(func(nt, usn, location string) []byte {
		return buildNotifyAlive(nt, usn, location, s.server)
	})
}

// announceByebye sends ssdp:byebye for every target on every interface.
func (s *Server) announceByebye() {
	s.sendAll(func(nt, usn, location string) []byte {
		return buildNotifyByebye(nt, usn)
	})
}

func (s *Server) sendAll(build func(nt, usn, location string) []byte) {
	s.mu.Lock()
	conn, pc, ifaces := s.conn, s.pc, s.ifaces
	s.mu.Unlock()
	if conn == nil || pc == nil {
		return
	}
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, ifi := range ifaces {
		ip := ipv4Addr(*ifi)
		if ip == nil {
			continue
		}
		_ = pc.SetMulticastInterface(ifi)
		location := fmt.Sprintf("http://%s:%d/dlna/device.xml", ip, s.cfg.Port)
		for _, pair := range ssdpSearchTargets(s.udn) {
			_, _ = conn.WriteToUDP(build(pair[0], pair[1], location), dst)
		}
	}
}

// Stop sends byebye announcements and closes all sockets. Idempotent.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conn, listeners := s.conn, s.listeners
		s.mu.Unlock()

		// byebye must go out while the socket and interfaces are still set —
		// sendAll reads them under the lock.
		s.announceByebye()

		s.mu.Lock()
		s.conn, s.pc, s.listeners, s.ifaces = nil, nil, nil, nil
		s.mu.Unlock()

		if conn != nil {
			conn.Close()
		}
		for _, l := range listeners {
			l.Close()
		}
		close(s.stopped)
	})
	return nil
}
