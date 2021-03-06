package coordinator

import (
	"bytes"
	"distributed/dto"
	"distributed/qutils"
	"encoding/gob"
	"fmt"

	"github.com/streadway/amqp"
)

// url to rabbit's end point so that
// connections can be established to it.
const url = "amqp://guest:guest@localhost:5672"

// QueueListener contains the logic that discovers the data queues,
// recevices the messages and eventually transales them to events
// in an EventAggregator
type QueueListener struct {
	conn *amqp.Connection // for getting messages
	ch   *amqp.Channel    // for getting messages

	// to prevent registering a seneor twice, to close of listener if associated sensor goes offline.
	// map that points to the Delivery objects
	sources map[string]<-chan amqp.Delivery //registry of all the sources that the coordinator is listening on

	// wiring the event aggregator in to the queue listener
	ea *EventAggregator
}

// NewQueueListener is a constructor function
// that ensures that the QueueListener is
// properly initialized
func NewQueueListener(ea *EventAggregator) *QueueListener {
	//instantiating new object of queuelistener
	ql := QueueListener{
		sources: make(map[string]<-chan amqp.Delivery),
		ea:      ea,
	}

	//populating the Connection and Channel fields
	ql.conn, ql.ch = qutils.GetChannel(url)

	return &ql
}

// DiscoverSensors is responsible for instantiating the fanout
// exchange to which the coordinators make a discovery requests
// to which the sensors respond by publishing the name of its
// data queues to the fanout exchange.
func (ql *QueueListener) DiscoverSensors() {
	// Setting up the new exchange
	ql.ch.ExchangeDeclare(
		qutils.SensorDiscoveryExchange, // name string, // name of the exchange that gets created
		"fanout",                       // kind string, // type which can be direct, topic, header or fanout
		false,                          // durable bool,	//sets up a durable queue or not
		false,                          // autodelete bool,	// if the exchange should be deleted in the event that there are no bindings present
		false,                          // internal bool,	true, if be need to reject external publishing requests
		false,                          // nowait bool,
		nil)                            // args Table)

	// exchange has been set up and now coordinators can publish to it.
	// as there is no meaningful info to send, sending an expty publishing
	// to the exchange
	ql.ch.Publish(
		qutils.SensorDiscoveryExchange, // exchange string,
		"",                             // key string,
		false,                          // mandatory bool,
		false,                          // immediate bool,
		amqp.Publishing{})              // msg Publishing)

}

// ListenForNewSource is responsible for
// letting the QueueListener discover new sensors
func (ql *QueueListener) ListenForNewSource() {
	// listening for data queue names that are being published by the sensors
	// when they come online or in response to a discovery request,
	// passing true for auto delete so that these temp queues are cleaned up.
	q := qutils.GetQueue("", ql.ch, true) // blank name for queue gives a random (unique) name to the queue (no conflicts when multiple coordinators are running)

	// By default the queue generated is bound to the default exchange.
	// the sensors publish to a fanout exchange, therefore, q needs to
	// rebind to that one.
	ql.ch.QueueBind(
		q.Name,       // name string,
		"",           // key string,
		"amq.fanout", // exchange string,
		false,        // noWait bool,
		nil)          // args amqp.Table

	// Receiver for consuming the messages
	msgs, _ := ql.ch.Consume(
		q.Name, //name of the queue bound to the fanout exchange
		"",
		true,
		false,
		false,
		false,
		nil)

	// dicovering sensors here, as we know for sure
	// that coordinator is expecting msgs from sensors
	ql.DiscoverSensors()
	fmt.Println("listening for new sources")

	// Channel in place, waiting for the messages on the msgs channel
	for msg := range msgs {
		fmt.Printf("Message %v", msg)
		// updated the if guard below to surround all
		// of the for-loops contents to prevent
		// same sensor being registered multiple
		// times with RabbitMQ
		//
		//
		// Sending data in a default exchange, this is a direct exchange
		// and will only deliver a message to a single receiver. That means
		// when multiple coordinatos are registered, they share access to the queues
		// when this happens, rabbitmq will take turns delivering to each registers
		// receiver in turn. This lets us scale the coordinators as the system grows
		// without affecting the rest of the system.

		//checking if new message has already been registered
		if ql.sources[string(msg.Body)] == nil {
			fmt.Println("new source discovered")

			// Publishing an event every time a new data source is discovered.
			ql.ea.PublishEvent("DataSourceDiscovered", string(msg.Body))
			// new message mesans a new sensor is online
			// and is ready to send the readings.
			// usind consume method to get access to that queue.
			sourceChan, _ := ql.ch.Consume(
				string(msg.Body), //name is in the msg body for the data queue.
				"",
				true,
				false,
				false,
				false,
				nil)
			ql.sources[string(msg.Body)] = sourceChan

			go ql.AddListener(sourceChan)
		}
	}
}

// AddListener is responsible
func (ql *QueueListener) AddListener(msgs <-chan amqp.Delivery) {
	// waiting for messages from the channel
	for msg := range msgs {
		// publish events for the downstream consumers
		// convert binary data to workable data
		r := bytes.NewReader(msg.Body)
		d := gob.NewDecoder(r)
		sd := new(dto.SensorMessage)
		d.Decode(sd)

		fmt.Printf("Received message: %v\n", sd)

		// Creating event data object and populating
		// it with the data from the sensor message
		// object
		ed := EventData{
			Name:      sd.Name,
			Timestamp: sd.Timestamp,
			Value:     sd.Value,
		}

		// Publishing to all the consumers so that
		// they now know what just haappened
		//
		// adding routing key (name of the sensor) so that each
		// one publishes a usique event
		ql.ea.PublishEvent("MessageReceived_"+msg.RoutingKey, ed)
	}
}
