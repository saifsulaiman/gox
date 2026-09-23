package main

import (
	"reflect"
	"unsafe"
)

type Node struct {
	Next *Node
}

type Consumer interface {
	Consume(*Node)
}

var global *Node

func leaf() *Node { return &Node{} }

func relay() *Node { return leaf() }

func graph() []*Node {
	node := &Node{}
	return []*Node{node}
}

func closure() func() *Node {
	node := &Node{}
	return func() *Node { return node }
}

func concurrent(done chan struct{}) {
	node := &Node{}
	go func() {
		global = node
		close(done)
	}()
}

func channel(ch chan<- *Node) { ch <- &Node{} }

func dynamic(consumer Consumer) { consumer.Consume(&Node{}) }

func boundaries() unsafe.Pointer {
	node := &Node{}
	reflect.ValueOf(node)
	return unsafe.Pointer(node)
}

type LargePacket struct {
	Data [70000]byte
	Seq  int
}

//go:noinline
func inspectPacket(p *LargePacket) int {
	return int(p.Data[0]) + p.Seq
}

func uniquePipeline(n int) int {
	pkt := &LargePacket{Seq: n}
	if n <= 0 {
		return 0
	}
	pkt.Data[0] = byte(n)
	if n > 50 {
		return inspectPacket(pkt) * 2
	}
	return inspectPacket(pkt)
}

func main() {
	_ = relay()
	_ = graph()
	_ = closure()
	_ = uniquePipeline(42)
}
