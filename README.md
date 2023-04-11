# Gracefully restart the process

## How to use it

```go
package main

func main() {
	g, err := grace.NewNetGrace(grace.NetGraceOption{})
	if err != nil {
		return
	}
	defer g.Close()

	if _, err := g.Listen("tcp", "127.0.0.1:8080"); err != nil {
		return
	}

	ready, err := g.Ready()
	if err != nil {
		return
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case <-ready:
			signal.Stop(sig)
			return
		case s, _ := <-sig:
			switch s {
			case syscall.SIGHUP:
				grace.Fork()
			case syscall.SIGTERM, syscall.SIGINT:
				signal.Stop(sig)
				return
			}
		}
	}
}
```
