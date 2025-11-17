package bundlepool

type Config struct {
    GlobalSlots uint64
}

func (c *Config) sanitize() Config {
    cfg := *c
    if cfg.GlobalSlots == 0 {
        cfg.GlobalSlots = 1024
    }
    return cfg
}