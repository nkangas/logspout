package syslogamqp

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"log/syslog"
	"os"
	"strconv"
	"syscall"
	"text/template"
	"time"

	"github.com/gliderlabs/logspout/router"
	"github.com/streadway/amqp"
)

const defaultRetryCount = 10

var (
	hostname         string
	retryCount       uint
	econnResetErrStr string
)

func init() {
	hostname, _ = os.Hostname()
	econnResetErrStr = fmt.Sprintf("write: %s", syscall.ECONNRESET.Error())
	router.AdapterFactories.Register(NewSyslogAMQPAdapter, "syslogamqp")
	setRetryCount()
}

func setRetryCount() {
	if count, err := strconv.Atoi(getopt("RETRY_COUNT", strconv.Itoa(defaultRetryCount))); err != nil {
		retryCount = uint(defaultRetryCount)
	} else {
		retryCount = uint(count)
	}
	debug("setting retryCount to:", retryCount)
}

func getopt(name, dfault string) string {
	value := os.Getenv(name)
	if value == "" {
		value = dfault
	}
	return value
}

func debug(v ...interface{}) {
	if os.Getenv("DEBUG") != "" {
		log.Println(v...)
	}
}

// NewSyslogAMQPAdapter returnas a configured syslog.Adapter
func NewSyslogAMQPAdapter(route *router.Route) (router.LogAdapter, error) {
	//uri := "amqp://" + a.user + ":" + a.password + "@" + a.address
	transportName := route.AdapterTransport("tcp")

	if !(transportName == "tcp" || transportName == "tls") {
		return nil, errors.New("unsupported transport: " + route.Adapter + ". Supported transports are tcp and tls.")
	}

	transport, found := router.AdapterTransports.Lookup(transportName)
	if !found {
		return nil, errors.New("transport not found: " + route.Adapter)
	}


	scheme := "amqp://"
	/*
	if transportName == "tls" {
		scheme = "amqps://"
	}
  */

  amqpConfig := &amqp.Config{
		Dial: func (_, address string) (net.Conn, error) {

			return transport.Dial(address, route.Options)
		},
	}

  username := getopt("AMQP_USERNAME", "guest")
	password := getopt("AMQP_PASSWORD", "guest")
	amqpURI := scheme+username+":"+password+"@"+route.Address
	log.Println("amqpURI: " + amqpURI)
	connection, err := amqp.DialConfig(amqpURI, *amqpConfig)

	if err != nil {
		log.Printf("amqp.Dial: %s - " + route.Address, err)
		return nil, err
	}
	channel, err := connection.Channel()
	if err != nil {
			log.Printf("connection.Channel(): %s", err)
		return nil, err
	}

	format := getopt("SYSLOG_FORMAT", "rfc5424")
	priority := getopt("SYSLOG_PRIORITY", "{{.Priority}}")
	pid := getopt("SYSLOG_PID", "{{.Container.State.Pid}}")

	content, err := ioutil.ReadFile("/etc/host_hostname") // just pass the file name
	if err == nil && len(content) > 0{
		hostname = string(content) // convert content to a 'string'
	} else {
		hostname = getopt("SYSLOG_HOSTNAME", "{{.Container.Config.Hostname}}")
	}
	tag := getopt("SYSLOG_TAG", "{{.ContainerName}}"+route.Options["append_tag"])
	structuredData := getopt("SYSLOG_STRUCTURED_DATA", "")
	if route.Options["structured_data"] != "" {
		structuredData = route.Options["structured_data"]
	}
	data := getopt("SYSLOG_DATA", "{{.Data}}")
	timestamp := getopt("SYSLOG_TIMESTAMP", "{{.Timestamp}}")

	if structuredData == "" {
		structuredData = "-"
	} else {
		structuredData = fmt.Sprintf("[%s]", structuredData)
	}

	var tmplStr string
	switch format {
	case "rfc5424":
		tmplStr = fmt.Sprintf("<%s>1 %s %s %s %s - %s %s\n",
			priority, timestamp, hostname, tag, pid, structuredData, data)
	case "rfc3164":
		tmplStr = fmt.Sprintf("<%s>%s %s %s[%s]: %s\n",
			priority, timestamp, hostname, tag, pid, data)
	default:
		return nil, errors.New("unsupported syslog format: " + format)
	}
	tmpl, err := template.New("syslog").Parse(tmplStr)
	if err != nil {
		return nil, err
	}
    //{{index .Container.Config.Labels "com.peoplenet.logspout-amqp-routing-key"}}
	routingKeyTmpl, err := template.New("AMQP_ROUTING_KEY").Parse(getopt("AMQP_ROUTING_KEY", "default"))
	if err != nil {
		return nil, err
	}

	return &AMQPAdapter {
	  amqpURI:    amqpURI,
	  amqpConfig: amqpConfig,
		route:      route,
		channel:   channel,
		exchange:   getopt("AMQP_EXCHANGE", "logspout"),
		omitWithEmptyRoutingKey: getopt("AMQP_REQUIRE_ROUTING_KEY", "false") == "false",
		omitWithDefaultRoutingKey: getopt("AMQP_REQUIRE_NON_DEFAULT_ROUTING_KEY", "false") == "false",
		routingKeyTmpl: routingKeyTmpl,
		tmpl:       tmpl,

	}, nil
}

// Adapter publishes log output to an AMQP exchange in the Syslog format
type AMQPAdapter struct {
	amqpConfig *amqp.Config
	amqpURI    string
	transport  *router.AdapterTransport
	channel    *amqp.Channel
	exchange   string
	routingKeyTmpl *template.Template
	omitWithEmptyRoutingKey bool
	omitWithDefaultRoutingKey bool
	route      *router.Route
	tmpl       *template.Template
}

// Stream sends log data to a connection
func (a *AMQPAdapter) Stream(logstream chan *router.Message) {
	for message := range logstream {
		m := &Message{message}
		buf, err := m.Render(a.tmpl)
		if err != nil {
			log.Println("syslog:", err)
			return
		}

		amqpMessage := amqp.Publishing{
			//Headers: amqp.Table{},
			//ContentType: "text/plain",
			//ContentEncoding: "UTF-8",
			//DeliveryMode: amqp.Transient,
			DeliveryMode: amqp.Persistent,
			Priority: 0,
			Timestamp: time.Now(),
			Body:buf,
		}

		routingKeyBuf, err := m.Render(a.routingKeyTmpl)
		var routingKey string
		if err != nil {
			log.Println("Error executing Routing Key template, using \"default\" as the routing key:", err)
			routingKey = "default"
		} else {
			routingKey = string(routingKeyBuf)
		}

		if routingKey == "" && a.omitWithEmptyRoutingKey {
			continue
		}
		if routingKey == "default" && a.omitWithDefaultRoutingKey {
			continue
		}


		err = a.channel.Publish(
			a.exchange, // exchange
			routingKey, // routing key
			false, // mandatory
			false, //immediate
			amqpMessage,
		)

		if err != nil {
			if err = a.retry(amqpMessage, routingKey, err); err != nil {
				log.Println("syslog retry err:", err)
				return
			}
		}
	}
}

func (a *AMQPAdapter) retry(amqpMessage amqp.Publishing, routingKey string, err error) error {
	if opError, ok := err.(*net.OpError); ok {
		if (opError.Temporary() && opError.Err.Error() != econnResetErrStr) || opError.Timeout() {
			retryErr := a.retryTemporary(amqpMessage, routingKey)
			if retryErr == nil {
				return nil
			}
		}
	}
	if reconnErr := a.reconnect(); reconnErr != nil {
		return reconnErr
	}
	if err = a.retryTemporary(amqpMessage, routingKey); err != nil {
		log.Println("syslog: reconnect failed")
		return err
	}
	log.Println("syslog: reconnect successful")
	return nil
}

func (a *AMQPAdapter) retryTemporary(amqpMessage amqp.Publishing, routingKey string) error {
	log.Printf("syslog: retrying amqp publish up to %v times\n", retryCount)
	err := retryExp(func() error {

		err := a.channel.Publish(
			a.exchange, // exchange
			routingKey, // routing key
			false, // mandatory
			false, //immediate
			amqpMessage,
		)
		if err == nil {
			log.Println("syslog: retry successful")
			return nil
		}

		return err
	}, retryCount)

	if err != nil {
		log.Println("syslog: retry failed")
		return err
	}

	return nil
}

func (a *AMQPAdapter) reconnect() error {
	log.Printf("syslog: reconnecting up to %v times\n", retryCount)
	err := retryExp(func() error {
		connection, err := amqp.DialConfig(a.amqpURI, *a.amqpConfig)

		if err != nil {
			log.Printf("amqp.Dial: %s - " + a.route.Address, err)
			return err
		}
		channel, err := connection.Channel()
		if err != nil {
			log.Printf("connection.Channel(): %s", err)
			return err
		}

		a.channel = channel
		return nil
	}, retryCount)

	if err != nil {
		return err
	}
	return nil
}

func retryExp(fun func() error, tries uint) error {
	try := uint(0)
	for {
		err := fun()
		if err == nil {
			return nil
		}

		try++
		if try > tries {
			return err
		}

		time.Sleep((1 << try) * 10 * time.Millisecond)
	}
}

// Message extends router.Message for the syslog standard
type Message struct {
	*router.Message
}

// Render transforms the log message using the Syslog template
func (m *Message) Render(tmpl *template.Template) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := tmpl.Execute(buf, m)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Priority returns a syslog.Priority based on the message source
func (m *Message) Priority() syslog.Priority {
	switch m.Message.Source {
	case "stdout":
		return syslog.LOG_USER | syslog.LOG_INFO
	case "stderr":
		return syslog.LOG_USER | syslog.LOG_ERR
	default:
		return syslog.LOG_DAEMON | syslog.LOG_INFO
	}
}

// Hostname returns the os hostname
func (m *Message) Hostname() string {
	return hostname
}

// Timestamp returns the message's syslog formatted timestamp
func (m *Message) Timestamp() string {
	return m.Message.Time.Format(time.RFC3339)
}

// ContainerName returns the message's container name
func (m *Message) ContainerName() string {
	return m.Message.Container.Name[1:]
}
