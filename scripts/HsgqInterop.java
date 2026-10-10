// Read-only interoperability probe using the FTTH application's own packaged
// HsgqEponSnmpAdapter and SNMP4J. JDK 21 is only needed to compile this probe;
// Fiberlab itself has no Java dependency. See docs/hsgq.md.
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.zip.ZipFile;

public final class HsgqInterop {
    public static void main(String[] args) throws Exception {
        if (args.length != 3) {
            throw new IllegalArgumentException("Usage: HsgqInterop FTTH_BOOT_JAR HOST PORT; community in FTTH_SNMP_COMMUNITY");
        }
        String community = System.getenv("FTTH_SNMP_COMMUNITY");
        if (community == null || community.isEmpty()) {
            throw new IllegalArgumentException("FTTH_SNMP_COMMUNITY must be set");
        }
        Path temp = Files.createTempDirectory("fiberlab-hsgq-probe-");
        List<Path> extracted = new ArrayList<>();
        try {
            List<URL> urls = new ArrayList<>();
            try (ZipFile zip = new ZipFile(args[0])) {
                var entries = zip.entries();
                while (entries.hasMoreElements()) {
                    var entry = entries.nextElement();
                    String name = entry.getName();
                    if (!name.startsWith("BOOT-INF/lib/") || !name.endsWith(".jar")) continue;
                    String leaf = Path.of(name).getFileName().toString();
                    if (!(leaf.startsWith("snmp-") || leaf.startsWith("contract-") ||
                          leaf.startsWith("snmp4j-") || leaf.startsWith("kotlin-stdlib-") ||
                          leaf.startsWith("slf4j-api-") || leaf.startsWith("kotlinx-serialization-core-"))) continue;
                    Path file = temp.resolve(leaf);
                    try (var input = zip.getInputStream(entry)) { Files.copy(input, file); }
                    extracted.add(file);
                    urls.add(file.toUri().toURL());
                }
            }
            // Isolate the probe from any unrelated application classpath.
            try (URLClassLoader loader = new URLClassLoader(urls.toArray(URL[]::new), ClassLoader.getPlatformClassLoader())) {
                Class<?> targetType = loader.loadClass("com.duluin.ftth.contract.OltTarget");
                // Deployed FTTH versions add nullable provisioning metadata
                // after the six SNMP fields; polling does not require it.
                Class<?>[] snmpFields = {String.class, String.class, String.class, String.class, int.class, String.class};
                Object target = null;
                for (var constructor : targetType.getConstructors()) {
                    Class<?>[] types = constructor.getParameterTypes();
                    if (constructor.isSynthetic() || types.length < 6 ||
                        !Arrays.equals(Arrays.copyOf(types, 6), snmpFields) ||
                        Arrays.stream(types).skip(6).anyMatch(Class::isPrimitive)) continue;
                    Object[] values = new Object[types.length];
                    System.arraycopy(new Object[]{"fiberlab", "FIBERLAB", "HSGQ", args[1], Integer.parseInt(args[2]), community}, 0, values, 0, 6);
                    target = constructor.newInstance(values);
                    break;
                }
                if (target == null) throw new IllegalStateException("Unsupported FTTH OltTarget constructor");
                Class<?> adapterType = loader.loadClass("com.duluin.ftth.snmp.HsgqEponSnmpAdapter");
                Object adapter = adapterType.getConstructor().newInstance();
                Object probe = adapterType.getMethod("probe", targetType).invoke(adapter, target);
                if (!probe.getClass().getSimpleName().equals("Reachable")) {
                    throw new IllegalStateException("FTTH adapter could not reach simulator: " + probe);
                }
                List<?> readings = (List<?>) adapterType.getMethod("pollOnus", targetType).invoke(adapter, target);
                for (Object reading : readings) {
                    Class<?> type = reading.getClass();
                    String[] getters = {"getSerialNumber", "getStatus", "getPonPortLabel", "getRxPowerDbm", "getTxPowerDbm"};
                    List<String> fields = new ArrayList<>();
                    for (String getter : getters) fields.add(String.valueOf(type.getMethod(getter).invoke(reading)));
                    System.out.println(String.join("\t", fields));
                }
                System.err.println("FTTH HSGQ adapter: " + readings.size() + " ONUs");
            }
        } finally {
            for (Path path : extracted) Files.deleteIfExists(path);
            Files.deleteIfExists(temp);
        }
    }
}
