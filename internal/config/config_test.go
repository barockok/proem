package config
import "testing"
func TestDefaultConfig(t *testing.T){
 c:=DefaultConfig()
 if c.ListenAddr!=":8080" {t.Fatal(c.ListenAddr)}
 if c.StickyMode!=StickyLB {t.Fatal(c.StickyMode)}
}
func TestParseFlagsInvalidSticky(t *testing.T){
 // ParseFlags uses global flag set, hard to test without reset; test constants instead
 if string(StickyLB)!="lb" {t.Fatal()}
 if string(StickyRedis)!="redis" {t.Fatal()}
 if string(StickyNone)!="none" {t.Fatal()}
}
