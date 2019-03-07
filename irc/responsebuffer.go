// Copyright (c) 2016-2017 Daniel Oaks <daniel@danieloaks.net>
// released under the MIT license

package irc

import (
	"runtime/debug"
	"time"

	"github.com/goshuirc/irc-go/ircmsg"
	"github.com/oragono/oragono/irc/caps"
	"github.com/oragono/oragono/irc/utils"
)

const (
	// https://ircv3.net/specs/extensions/labeled-response.html
	defaultBatchType = "draft/labeled-response"
)

// ResponseBuffer - put simply - buffers messages and then outputs them to a given client.
//
// Using a ResponseBuffer lets you really easily implement labeled-response, since the
// buffer will silently create a batch if required and label the outgoing messages as
// necessary (or leave it off and simply tag the outgoing message).
type ResponseBuffer struct {
	Label     string
	batchID   string
	target    *Client
	messages  []ircmsg.IrcMessage
	finalized bool
}

// GetLabel returns the label from the given message.
func GetLabel(msg ircmsg.IrcMessage) string {
	_, value := msg.GetTag(caps.LabelTagName)
	return value
}

// NewResponseBuffer returns a new ResponseBuffer.
func NewResponseBuffer(target *Client) *ResponseBuffer {
	return &ResponseBuffer{
		target: target,
	}
}

func (rb *ResponseBuffer) AddMessage(msg ircmsg.IrcMessage) {
	if rb.finalized {
		rb.target.server.logger.Error("internal", "message added to finalized ResponseBuffer, undefined behavior")
		debug.PrintStack()
		// TODO(dan): send a NOTICE to the end user with a string representation of the message,
		// for debugging purposes
		return
	}

	rb.messages = append(rb.messages, msg)
}

// Add adds a standard new message to our queue.
func (rb *ResponseBuffer) Add(tags map[string]string, prefix string, command string, params ...string) {
	rb.AddMessage(ircmsg.MakeMessage(tags, prefix, command, params...))
}

// AddFromClient adds a new message from a specific client to our queue.
func (rb *ResponseBuffer) AddFromClient(msgid string, fromNickMask string, fromAccount string, tags map[string]string, command string, params ...string) {
	msg := ircmsg.MakeMessage(nil, fromNickMask, command, params...)
	msg.UpdateTags(tags)

	// attach account-tag
	if rb.target.capabilities.Has(caps.AccountTag) && fromAccount != "*" {
		msg.SetTag("account", fromAccount)
	}
	// attach message-id
	if len(msgid) > 0 && rb.target.capabilities.Has(caps.MessageTags) {
		msg.SetTag("draft/msgid", msgid)
	}

	rb.AddMessage(msg)
}

// AddSplitMessageFromClient adds a new split message from a specific client to our queue.
func (rb *ResponseBuffer) AddSplitMessageFromClient(fromNickMask string, fromAccount string, tags map[string]string, command string, target string, message utils.SplitMessage) {
	if rb.target.capabilities.Has(caps.MaxLine) || message.Wrapped == nil {
		rb.AddFromClient(message.Msgid, fromNickMask, fromAccount, tags, command, target, message.Message)
	} else {
		for _, messagePair := range message.Wrapped {
			rb.AddFromClient(messagePair.Msgid, fromNickMask, fromAccount, tags, command, target, messagePair.Message)
		}
	}
}

// InitializeBatch forcibly starts a batch of batch `batchType`.
// Normally, Send/Flush will decide automatically whether to start a batch
// of type draft/labeled-response. This allows changing the batch type
// and forcing the creation of a possibly empty batch.
func (rb *ResponseBuffer) InitializeBatch(batchType string, blocking bool) {
	rb.sendBatchStart(batchType, blocking)
}

func (rb *ResponseBuffer) sendBatchStart(batchType string, blocking bool) {
	if rb.batchID != "" {
		// batch already initialized
		return
	}

	// formerly this combined time.Now.UnixNano() in base 36 with an incrementing counter,
	// also in base 36. but let's just use a uuidv4-alike (26 base32 characters):
	rb.batchID = utils.GenerateSecretToken()

	message := ircmsg.MakeMessage(nil, rb.target.server.name, "BATCH", "+"+rb.batchID, batchType)
	if rb.Label != "" {
		message.SetTag(caps.LabelTagName, rb.Label)
	}
	rb.target.SendRawMessage(message, blocking)
}

func (rb *ResponseBuffer) sendBatchEnd(blocking bool) {
	if rb.batchID == "" {
		// we are not sending a batch, skip this
		return
	}

	message := ircmsg.MakeMessage(nil, rb.target.server.name, "BATCH", "-"+rb.batchID)
	rb.target.SendRawMessage(message, blocking)
}

// Send sends all messages in the buffer to the client.
// Afterwards, the buffer is in an undefined state and MUST NOT be used further.
// If `blocking` is true you MUST be sending to the client from its own goroutine.
func (rb *ResponseBuffer) Send(blocking bool) error {
	return rb.flushInternal(true, blocking)
}

// Flush sends all messages in the buffer to the client.
// Afterwards, the buffer can still be used. Client code MUST subsequently call Send()
// to ensure that the final `BATCH -` message is sent.
// If `blocking` is true you MUST be sending to the client from its own goroutine.
func (rb *ResponseBuffer) Flush(blocking bool) error {
	return rb.flushInternal(false, blocking)
}

// flushInternal sends the contents of the buffer, either blocking or nonblocking
// It sends the `BATCH +` message if the client supports it and it hasn't been sent already.
// If `final` is true, it also sends `BATCH -` (if necessary).
func (rb *ResponseBuffer) flushInternal(final bool, blocking bool) error {
	if rb.finalized {
		return nil
	}

	useLabel := rb.target.capabilities.Has(caps.LabeledResponse) && rb.Label != ""
	// use a batch if we have a label, and we either currently have 0 or 2+ messages,
	// or we are doing a Flush() and we have to assume that there will be more messages
	// in the future.
	useBatch := useLabel && (len(rb.messages) != 1 || !final)

	// if label but no batch, add label to first message
	if useLabel && !useBatch && len(rb.messages) == 1 && rb.batchID == "" {
		rb.messages[0].SetTag(caps.LabelTagName, rb.Label)
	} else if useBatch {
		rb.sendBatchStart(defaultBatchType, blocking)
	}

	// send each message out
	for _, message := range rb.messages {
		// attach server-time if needed
		if rb.target.capabilities.Has(caps.ServerTime) && !message.HasTag("time") {
			message.SetTag("time", time.Now().UTC().Format(IRCv3TimestampFormat))
		}

		// attach batch ID
		if rb.batchID != "" {
			message.SetTag("batch", rb.batchID)
		}

		// send message out
		rb.target.SendRawMessage(message, blocking)
	}

	// end batch if required
	if final {
		rb.sendBatchEnd(blocking)
		rb.finalized = true
	}

	// clear out any existing messages
	rb.messages = rb.messages[:0]

	return nil
}

// Notice sends the client the given notice from the server.
func (rb *ResponseBuffer) Notice(text string) {
	rb.Add(nil, rb.target.server.name, "NOTICE", rb.target.nick, text)
}
