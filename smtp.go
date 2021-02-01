package gomail

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

var defaultDialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
}

// A Dialer is a dialer to an SMTP server.
type Dialer struct {
	// Host represents the host of the SMTP server.
	Host string
	// Port represents the port of the SMTP server.
	Port int
	// Username is the username to use to authenticate to the SMTP server.
	Username string
	// Password is the password to use to authenticate to the SMTP server.
	Password string
	// Auth represents the authentication mechanism used to authenticate to the
	// SMTP server.
	Auth sasl.Client
	// SSL defines whether an SSL connection is used. It should be false in
	// most cases since the authentication mechanism should use the STARTTLS
	// extension instead.
	SSL bool
	// TSLConfig represents the TLS configuration used for the TLS (when the
	// STARTTLS extension is used) or SSL connection.
	TLSConfig *tls.Config
	// LocalName is the hostname sent to the SMTP server with the HELO command.
	// By default, "localhost" is sent.
	LocalName string
	// Options
	MailOptions *smtp.MailOptions

	dialer netDialer
}

// NewWithDialer returns a new SMTP Dialer configured with the provided net.Dialer.
func NewWithDialer(dialer *net.Dialer, host string, port int, username, password string) *Dialer {
	return &Dialer{
		dialer:   dialer,
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      port == 465,
	}
}

// NewDialer returns a new SMTP Dialer. The given parameters are used to connect
// to the SMTP server.
func NewDialer(host string, port int, username, password string) *Dialer {
	return &Dialer{
		dialer:   defaultDialer,
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      port == 465,
	}
}

// NewPlainDialer returns a new SMTP Dialer. The given parameters are used to
// connect to the SMTP server.
//
// Deprecated: Use NewDialer instead.
func NewPlainDialer(host string, port int, username, password string) *Dialer {
	return NewDialer(host, port, username, password)
}

// Dial dials and authenticates to an SMTP server. The returned SendCloser
// should be closed when done using it.
func (d *Dialer) Dial() (SendCloser, error) {
	if d.dialer == nil {
		d.dialer = defaultDialer
	}
	conn, err := d.dialer.Dial("tcp", addr(d.Host, d.Port))
	if err != nil {
		return nil, err
	}

	if d.SSL {
		conn = tlsClient(conn, d.tlsConfig())
	}

	c, err := smtpNewClient(conn, d.Host)
	if err != nil {
		return nil, err
	}

	if d.LocalName != "" {
		if err := c.Hello(d.LocalName); err != nil {
			return nil, err
		}
	}

	if !d.SSL {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(d.tlsConfig()); err != nil {
				c.Close()
				return nil, err
			}
		}
	}

	if d.Auth == nil && d.Username != "" {
		if ok, auths := c.Extension("AUTH"); ok {
			if strings.Contains(auths, "LOGIN") &&
				!strings.Contains(auths, "PLAIN") {
				d.Auth = sasl.NewLoginClient(d.Username, d.Password)
			} else {
				d.Auth = sasl.NewPlainClient("", d.Username, d.Password)
			}
		}
	}

	if d.Auth != nil {
		if err = c.Auth(d.Auth); err != nil {
			c.Close()
			return nil, err
		}
	}

	return &smtpSender{c, d}, nil
}

func (d *Dialer) tlsConfig() *tls.Config {
	if d.TLSConfig == nil {
		return &tls.Config{ServerName: d.Host}
	}
	return d.TLSConfig
}

func (d *Dialer) Options() *smtp.MailOptions {
	if d.MailOptions == nil {
		return &smtp.MailOptions{UTF8: true}
	}
	return d.MailOptions
}

func addr(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

// DialAndSend opens a connection to the SMTP server, sends the given emails and
// closes the connection.
func (d *Dialer) DialAndSend(m ...*Message) error {
	s, err := d.Dial()
	if err != nil {
		return err
	}
	defer s.Close()

	return Send(s, m...)
}

type smtpSender struct {
	smtpClient
	d *Dialer
}

func (c *smtpSender) Send(from string, to []string, msg io.WriterTo) error {
	options := c.d.Options()
	if err := c.Mail(from, options); err != nil {
		if err == io.EOF {
			// This is probably due to a timeout, so reconnect and try again.
			sc, derr := c.d.Dial()
			if derr == nil {
				if s, ok := sc.(*smtpSender); ok {
					*c = *s
					return c.Send(from, to, msg)
				}
			}
		}
		return err
	}

	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		return err
	}

	if _, err = msg.WriteTo(w); err != nil {
		w.Close()
		return err
	}

	return w.Close()
}

func (c *smtpSender) Close() error {
	return c.Quit()
}

func (c *smtpSender) Reset() error {
	return c.smtpClient.Reset()
}

type netDialer interface {
	Dial(network, address string) (net.Conn, error)
}

// Stubbed out for tests.
var (
	tlsClient     = tls.Client
	smtpNewClient = func(conn net.Conn, host string) (smtpClient, error) {
		return smtp.NewClient(conn, host)
	}
)

type smtpClient interface {
	Hello(string) error
	Extension(string) (bool, string)
	StartTLS(*tls.Config) error
	Auth(sasl.Client) error
	Mail(string, *smtp.MailOptions) error
	Rcpt(string) error
	Reset() error
	Data() (io.WriteCloser, error)
	Quit() error
	Close() error
}
