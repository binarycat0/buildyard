package dev.buildyard.ext;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStreamWriter;
import java.io.Writer;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import javax.inject.Named;
import javax.inject.Singleton;

import org.apache.maven.eventspy.AbstractEventSpy;
import org.apache.maven.execution.ExecutionEvent;
import org.apache.maven.execution.MavenSession;
import org.apache.maven.execution.ProjectDependencyGraph;
import org.apache.maven.plugin.MojoExecution;
import org.apache.maven.project.MavenProject;

/**
 * Writes Maven build events as JSON Lines to the file named by -Dbuildyard.events.
 * Does nothing without that property, so it is harmless if loaded elsewhere.
 */
@Named("buildyard")
@Singleton
public class BuildyardEventSpy extends AbstractEventSpy {
    private Writer out;
    private final Map<String, Long> started = new HashMap<>();

    @Override
    public void init(Context context) throws Exception {
        Object props = context.getData().get("userProperties");
        String path = null;
        if (props instanceof Map) {
            Object v = ((Map<?, ?>) props).get("buildyard.events");
            if (v != null) {
                path = v.toString();
            }
        }
        if (path == null) {
            path = System.getProperty("buildyard.events");
        }
        if (path != null && !path.isEmpty()) {
            File f = new File(path);
            if (f.getParentFile() != null) {
                f.getParentFile().mkdirs();
            }
            out = new OutputStreamWriter(new FileOutputStream(f, true), StandardCharsets.UTF_8);
        }
    }

    @Override
    public void onEvent(Object event) throws Exception {
        if (out == null || !(event instanceof ExecutionEvent)) {
            return;
        }
        ExecutionEvent e = (ExecutionEvent) event;
        switch (e.getType()) {
            case SessionStarted:
                session(e.getSession());
                break;
            case ProjectStarted:
                started.put(key(e.getProject()), System.currentTimeMillis());
                write("{\"t\":\"project_start\",\"key\":" + str(key(e.getProject())) + ",\"ts\":" + System.currentTimeMillis() + "}");
                break;
            case ProjectSucceeded:
                projectEnd(e.getProject(), "succeeded");
                break;
            case ProjectFailed:
                projectEnd(e.getProject(), "failed");
                break;
            case ProjectSkipped:
                projectEnd(e.getProject(), "skipped");
                break;
            case MojoStarted:
                MojoExecution m = e.getMojoExecution();
                if (m != null && e.getProject() != null) {
                    write("{\"t\":\"mojo_start\",\"key\":" + str(key(e.getProject())) + ",\"mojo\":"
                            + str(m.getArtifactId() + ":" + m.getGoal()) + ",\"id\":" + str(m.getExecutionId())
                            + ",\"ts\":" + System.currentTimeMillis() + "}");
                }
                break;
            case SessionEnded:
                MavenSession s = e.getSession();
                boolean failed = s != null && s.getResult() != null && s.getResult().hasExceptions();
                write("{\"t\":\"session_end\",\"ts\":" + System.currentTimeMillis() + ",\"status\":"
                        + str(failed ? "failed" : "succeeded") + "}");
                break;
            default:
                break;
        }
    }

    @Override
    public void close() throws Exception {
        if (out != null) {
            out.close();
            out = null;
        }
    }

    /** The reactor: each project's folder, aggregating parent, and direct in-reactor dependencies. */
    private void session(MavenSession s) throws IOException {
        if (s == null) {
            return;
        }
        List<MavenProject> projects = s.getProjects();
        ProjectDependencyGraph graph = s.getProjectDependencyGraph();
        // The aggregator of a module is the project whose <modules> point at its folder.
        Map<File, MavenProject> byDir = new HashMap<>();
        for (MavenProject p : projects) {
            byDir.put(canonical(p.getBasedir()), p);
        }
        Map<MavenProject, MavenProject> aggregator = new HashMap<>();
        for (MavenProject p : projects) {
            for (String module : p.getModules()) {
                File dir = canonical(new File(p.getBasedir(), module));
                if (dir.isFile()) {
                    dir = dir.getParentFile();
                }
                MavenProject child = byDir.get(dir);
                if (child != null && child != p) {
                    aggregator.put(child, p);
                }
            }
        }
        StringBuilder b = new StringBuilder("{\"t\":\"session\",\"ts\":").append(System.currentTimeMillis());
        File root = s.getExecutionRootDirectory() != null ? canonical(new File(s.getExecutionRootDirectory())) : null;
        if (root != null) {
            b.append(",\"root\":").append(str(root.getPath()));
        }
        b.append(",\"projects\":[");
        for (int i = 0; i < projects.size(); i++) {
            MavenProject p = projects.get(i);
            if (i > 0) {
                b.append(',');
            }
            b.append("{\"key\":").append(str(key(p)))
                    .append(",\"name\":").append(str(p.getName() != null ? p.getName() : p.getArtifactId()))
                    .append(",\"packaging\":").append(str(p.getPackaging()))
                    .append(",\"dir\":").append(str(canonical(p.getBasedir()).getPath()));
            MavenProject agg = aggregator.get(p);
            if (agg != null) {
                b.append(",\"aggregator\":").append(str(key(agg)));
            }
            MavenProject parent = p.getParent();
            if (parent != null && projects.contains(parent)) {
                b.append(",\"parent\":").append(str(key(parent)));
            }
            b.append(",\"deps\":[");
            if (graph != null) {
                List<MavenProject> up = graph.getUpstreamProjects(p, false);
                for (int j = 0; j < up.size(); j++) {
                    if (j > 0) {
                        b.append(',');
                    }
                    b.append(str(key(up.get(j))));
                }
            }
            b.append("]}");
        }
        b.append("]}");
        write(b.toString());
    }

    private void projectEnd(MavenProject p, String status) throws IOException {
        String k = key(p);
        Long t0 = started.remove(k);
        long ms = t0 == null ? 0 : System.currentTimeMillis() - t0;
        write("{\"t\":\"project_end\",\"key\":" + str(k) + ",\"status\":" + str(status) + ",\"ms\":" + ms
                + ",\"ts\":" + System.currentTimeMillis() + "}");
    }

    private synchronized void write(String line) throws IOException {
        out.write(line);
        out.write('\n');
        out.flush();
    }

    private static String key(MavenProject p) {
        return p == null ? "" : p.getGroupId() + ":" + p.getArtifactId();
    }

    private static File canonical(File f) {
        try {
            return f.getCanonicalFile();
        } catch (IOException e) {
            return f.getAbsoluteFile();
        }
    }

    static String str(String s) {
        if (s == null) {
            return "null";
        }
        StringBuilder b = new StringBuilder(s.length() + 2).append('"');
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
        return b.append('"').toString();
    }
}
