# pubsub

```go
pubsub := pubsub.NewPubSub[Message]()
subscriber := pubsub.Subscribe()

pubsub.Publish(Message{name: "Hello, world!"})

msg := <-subscriber.C
```
