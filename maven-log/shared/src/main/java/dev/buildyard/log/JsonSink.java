package dev.buildyard.log;

import java.io.BufferedWriter;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStreamWriter;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.io.Writer;
import java.nio.charset.StandardCharsets;
import java.util.Map;

import org.slf4j.MDC;

/**
 * Appends one JSON object per log call to the file named by -Dbuildyard.log:
 * {"ts":ms,"level":"INFO","logger":…,"thread":…,"msg":…,"mdc":{…},"exception":{"type","message","stacktrace"}}.
 * Without the property it does nothing. The path is read on every call: an mvnd daemon
 * serves many builds, each with its own file.
 */
public final class JsonSink {
    private static Writer out;
    private static String openPath;

    private JsonSink() {
    }

    public static void write(String level, String logger, String msg, Throwable t) {
        String path = System.getProperty("buildyard.log");
        if (path == null || path.isEmpty()) {
            return;
        }
        StringBuilder b = new StringBuilder(160);
        b.append("{\"ts\":").append(System.currentTimeMillis());
        b.append(",\"level\":");
        str(b, level);
        b.append(",\"logger\":");
        str(b, logger);
        b.append(",\"thread\":");
        str(b, Thread.currentThread().getName());
        b.append(",\"msg\":");
        str(b, msg == null ? "" : msg);
        Map<String, String> mdc = null;
        try {
            mdc = MDC.getCopyOfContextMap();
        } catch (RuntimeException | LinkageError ignore) {
            // no MDC adapter: records carry no context
        }
        if (mdc != null && !mdc.isEmpty()) {
            b.append(",\"mdc\":{");
            boolean first = true;
            for (Map.Entry<String, String> e : mdc.entrySet()) {
                if (!first) {
                    b.append(',');
                }
                first = false;
                str(b, e.getKey());
                b.append(':');
                str(b, e.getValue());
            }
            b.append('}');
        }
        if (t != null) {
            StringWriter sw = new StringWriter();
            t.printStackTrace(new PrintWriter(sw));
            b.append(",\"exception\":{\"type\":");
            str(b, t.getClass().getName());
            if (t.getMessage() != null) {
                b.append(",\"message\":");
                str(b, t.getMessage());
            }
            b.append(",\"stacktrace\":");
            str(b, sw.toString());
            b.append('}');
        }
        b.append("}\n");
        synchronized (JsonSink.class) {
            try {
                if (!path.equals(openPath)) {
                    if (out != null) {
                        out.close();
                    }
                    out = null;
                    openPath = path;
                    out = new BufferedWriter(new OutputStreamWriter(new FileOutputStream(path, true), StandardCharsets.UTF_8));
                }
                if (out != null) {
                    out.write(b.toString());
                    out.flush(); // Buildyard follows the file while the build runs
                }
            } catch (IOException e) {
                out = null; // give up on this file; the console log is unaffected
            }
        }
    }

    private static void str(StringBuilder b, String s) {
        b.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"':
                    b.append("\\\"");
                    break;
                case '\\':
                    b.append("\\\\");
                    break;
                case '\n':
                    b.append("\\n");
                    break;
                case '\r':
                    b.append("\\r");
                    break;
                case '\t':
                    b.append("\\t");
                    break;
                default:
                    if (c < 0x20) {
                        b.append(String.format("\\u%04x", (int) c));
                    } else {
                        b.append(c);
                    }
            }
        }
        b.append('"');
    }
}
