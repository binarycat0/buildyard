package dev.buildyard.log;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ConcurrentMap;

import org.slf4j.ILoggerFactory;
import org.slf4j.Logger;

/** Wraps Maven's logger factory: same console output, plus JSON lines. */
public final class TeeLoggerFactory implements ILoggerFactory {
    private final ILoggerFactory delegate;
    private final ConcurrentMap<String, Logger> loggers = new ConcurrentHashMap<>();

    public TeeLoggerFactory(ILoggerFactory delegate) {
        this.delegate = delegate;
    }

    @Override
    public Logger getLogger(String name) {
        Logger l = loggers.get(name);
        if (l == null) {
            Logger created = new TeeLogger(name, delegate.getLogger(name));
            l = loggers.putIfAbsent(name, created);
            if (l == null) {
                l = created;
            }
        }
        return l;
    }
}
