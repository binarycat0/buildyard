package dev.buildyard.log;

import org.slf4j.Logger;
import org.slf4j.helpers.FormattingTuple;
import org.slf4j.helpers.MarkerIgnoringBase;
import org.slf4j.helpers.MessageFormatter;

/** Logs through Maven's own logger (the console is unchanged) and writes the same call as JSON. */
final class TeeLogger extends MarkerIgnoringBase {
    private static final long serialVersionUID = 1L;

    enum Level { TRACE, DEBUG, INFO, WARN, ERROR }

    private final transient Logger d;

    TeeLogger(String name, Logger d) {
        this.name = name;
        this.d = d;
    }

    private void log(Level level, FormattingTuple ft) {
        log(level, ft.getMessage(), ft.getThrowable());
    }

    private void log(Level level, String msg, Throwable t) {
        StdoutCapture.ensure();
        StdoutCapture.enterLogger();
        try {
            switch (level) {
                case TRACE:
                    d.trace(msg, t);
                    break;
                case DEBUG:
                    d.debug(msg, t);
                    break;
                case INFO:
                    d.info(msg, t);
                    break;
                case WARN:
                    d.warn(msg, t);
                    break;
                default:
                    d.error(msg, t);
            }
        } finally {
            StdoutCapture.exitLogger();
        }
        JsonSink.write(level.name(), name, msg, t);
    }

    @Override
    public boolean isTraceEnabled() {
        return d.isTraceEnabled();
    }

    @Override
    public void trace(String msg) {
        if (d.isTraceEnabled()) {
            log(Level.TRACE, msg, null);
        }
    }

    @Override
    public void trace(String format, Object arg) {
        if (d.isTraceEnabled()) {
            log(Level.TRACE, MessageFormatter.format(format, arg));
        }
    }

    @Override
    public void trace(String format, Object arg1, Object arg2) {
        if (d.isTraceEnabled()) {
            log(Level.TRACE, MessageFormatter.format(format, arg1, arg2));
        }
    }

    @Override
    public void trace(String format, Object... args) {
        if (d.isTraceEnabled()) {
            log(Level.TRACE, MessageFormatter.arrayFormat(format, args));
        }
    }

    @Override
    public void trace(String msg, Throwable t) {
        if (d.isTraceEnabled()) {
            log(Level.TRACE, msg, t);
        }
    }

    @Override
    public boolean isDebugEnabled() {
        return d.isDebugEnabled();
    }

    @Override
    public void debug(String msg) {
        if (d.isDebugEnabled()) {
            log(Level.DEBUG, msg, null);
        }
    }

    @Override
    public void debug(String format, Object arg) {
        if (d.isDebugEnabled()) {
            log(Level.DEBUG, MessageFormatter.format(format, arg));
        }
    }

    @Override
    public void debug(String format, Object arg1, Object arg2) {
        if (d.isDebugEnabled()) {
            log(Level.DEBUG, MessageFormatter.format(format, arg1, arg2));
        }
    }

    @Override
    public void debug(String format, Object... args) {
        if (d.isDebugEnabled()) {
            log(Level.DEBUG, MessageFormatter.arrayFormat(format, args));
        }
    }

    @Override
    public void debug(String msg, Throwable t) {
        if (d.isDebugEnabled()) {
            log(Level.DEBUG, msg, t);
        }
    }

    @Override
    public boolean isInfoEnabled() {
        return d.isInfoEnabled();
    }

    @Override
    public void info(String msg) {
        if (d.isInfoEnabled()) {
            log(Level.INFO, msg, null);
        }
    }

    @Override
    public void info(String format, Object arg) {
        if (d.isInfoEnabled()) {
            log(Level.INFO, MessageFormatter.format(format, arg));
        }
    }

    @Override
    public void info(String format, Object arg1, Object arg2) {
        if (d.isInfoEnabled()) {
            log(Level.INFO, MessageFormatter.format(format, arg1, arg2));
        }
    }

    @Override
    public void info(String format, Object... args) {
        if (d.isInfoEnabled()) {
            log(Level.INFO, MessageFormatter.arrayFormat(format, args));
        }
    }

    @Override
    public void info(String msg, Throwable t) {
        if (d.isInfoEnabled()) {
            log(Level.INFO, msg, t);
        }
    }

    @Override
    public boolean isWarnEnabled() {
        return d.isWarnEnabled();
    }

    @Override
    public void warn(String msg) {
        if (d.isWarnEnabled()) {
            log(Level.WARN, msg, null);
        }
    }

    @Override
    public void warn(String format, Object arg) {
        if (d.isWarnEnabled()) {
            log(Level.WARN, MessageFormatter.format(format, arg));
        }
    }

    @Override
    public void warn(String format, Object arg1, Object arg2) {
        if (d.isWarnEnabled()) {
            log(Level.WARN, MessageFormatter.format(format, arg1, arg2));
        }
    }

    @Override
    public void warn(String format, Object... args) {
        if (d.isWarnEnabled()) {
            log(Level.WARN, MessageFormatter.arrayFormat(format, args));
        }
    }

    @Override
    public void warn(String msg, Throwable t) {
        if (d.isWarnEnabled()) {
            log(Level.WARN, msg, t);
        }
    }

    @Override
    public boolean isErrorEnabled() {
        return d.isErrorEnabled();
    }

    @Override
    public void error(String msg) {
        if (d.isErrorEnabled()) {
            log(Level.ERROR, msg, null);
        }
    }

    @Override
    public void error(String format, Object arg) {
        if (d.isErrorEnabled()) {
            log(Level.ERROR, MessageFormatter.format(format, arg));
        }
    }

    @Override
    public void error(String format, Object arg1, Object arg2) {
        if (d.isErrorEnabled()) {
            log(Level.ERROR, MessageFormatter.format(format, arg1, arg2));
        }
    }

    @Override
    public void error(String format, Object... args) {
        if (d.isErrorEnabled()) {
            log(Level.ERROR, MessageFormatter.arrayFormat(format, args));
        }
    }

    @Override
    public void error(String msg, Throwable t) {
        if (d.isErrorEnabled()) {
            log(Level.ERROR, msg, t);
        }
    }
}
