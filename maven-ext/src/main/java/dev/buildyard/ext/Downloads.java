package dev.buildyard.ext;

import java.io.File;
import java.io.IOException;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

import org.eclipse.aether.RepositoryEvent;
import org.eclipse.aether.artifact.Artifact;
import org.eclipse.aether.metadata.Metadata;
import org.eclipse.aether.repository.ArtifactRepository;
import org.eclipse.aether.repository.RemoteRepository;

/**
 * Turns resolver download events into "download" lines: what was fetched, from which
 * repository, for which module, how long it took and whether it was found. Kept in its own
 * class so a Maven without the resolver API on the extension class path only loses these.
 */
final class Downloads {
    private final BuildyardEventSpy spy;
    private final Map<String, Long> started = new ConcurrentHashMap<>();

    Downloads(BuildyardEventSpy spy) {
        this.spy = spy;
        RepositoryEvent.EventType.values(); // fail here, not mid-build, when the API is missing
    }

    void onEvent(Object event) throws IOException {
        if (!(event instanceof RepositoryEvent)) {
            return;
        }
        RepositoryEvent e = (RepositoryEvent) event;
        switch (e.getType()) {
            case ARTIFACT_DOWNLOADING:
            case METADATA_DOWNLOADING:
                started.put(id(e), System.currentTimeMillis());
                break;
            case ARTIFACT_DOWNLOADED:
            case METADATA_DOWNLOADED:
                done(e);
                break;
            default:
                break;
        }
    }

    private void done(RepositoryEvent e) throws IOException {
        long now = System.currentTimeMillis();
        Long t0 = started.remove(id(e));
        boolean artifact = e.getType() == RepositoryEvent.EventType.ARTIFACT_DOWNLOADED;
        File f = e.getFile();
        boolean ok = e.getException() == null && f != null && f.isFile();
        StringBuilder b = new StringBuilder("{\"t\":\"download\"");
        String project = spy.currentProject();
        if (project != null) {
            b.append(",\"key\":").append(BuildyardEventSpy.str(project));
        }
        b.append(",\"kind\":").append(BuildyardEventSpy.str(artifact ? "artifact" : "metadata"))
                .append(",\"name\":").append(BuildyardEventSpy.str(name(e)))
                .append(",\"repo\":").append(BuildyardEventSpy.str(repoId(e.getRepository())));
        if (e.getRepository() instanceof RemoteRepository) {
            b.append(",\"url\":").append(BuildyardEventSpy.str(((RemoteRepository) e.getRepository()).getUrl()));
        }
        b.append(",\"ok\":").append(ok);
        if (ok) {
            b.append(",\"bytes\":").append(f.length());
        }
        b.append(",\"ms\":").append(t0 == null ? 0 : now - t0).append(",\"ts\":").append(now).append('}');
        spy.write(b.toString());
    }

    private static String id(RepositoryEvent e) {
        return e.getType().name().startsWith("ARTIFACT") ? "a|" + name(e) + "|" + repoId(e.getRepository())
                : "m|" + name(e) + "|" + repoId(e.getRepository());
    }

    private static String name(RepositoryEvent e) {
        Artifact a = e.getArtifact();
        if (a != null) {
            String s = a.getGroupId() + ":" + a.getArtifactId() + ":" + a.getExtension();
            if (a.getClassifier() != null && !a.getClassifier().isEmpty()) {
                s += ":" + a.getClassifier();
            }
            return s + ":" + a.getVersion();
        }
        Metadata m = e.getMetadata();
        if (m != null) {
            StringBuilder s = new StringBuilder(m.getGroupId());
            if (!m.getArtifactId().isEmpty()) {
                s.append(':').append(m.getArtifactId());
            }
            if (!m.getVersion().isEmpty()) {
                s.append(':').append(m.getVersion());
            }
            return s.append(':').append(m.getType()).toString();
        }
        return "";
    }

    private static String repoId(ArtifactRepository r) {
        return r == null ? "" : r.getId();
    }
}
