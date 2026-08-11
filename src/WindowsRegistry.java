import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.nio.charset.Charset;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

public class WindowsRegistry {

    /**
     * 查询 Windows Registry。
     */
    public List<RegistryEntry> query(String registryPath) {

        List<RegistryEntry> result =
                new ArrayList<>();

        try {

            Process process =
                    new ProcessBuilder(
                            "reg",
                            "query",
                            registryPath,
                            "/s"
                    )
                            .redirectErrorStream(true)
                            .start();

            try (BufferedReader reader =
                         new BufferedReader(
                                 new InputStreamReader(
                                         process.getInputStream(),
                                         Charset.defaultCharset()
                                 ))) {

                RegistryEntry current = null;

                String line;

                while ((line = reader.readLine()) != null) {

                    line = line.trim();

                    if (line.isEmpty()) {
                        continue;
                    }

                    /*
                     * 新的 Registry Key
                     */
                    if (line.startsWith("HKEY_")) {

                        if (current != null) {
                            result.add(current);
                        }

                        current =
                                new RegistryEntry(line);

                        continue;
                    }

                    if (current == null) {
                        continue;
                    }

                    /*
                     * 例如：
                     *
                     * DisplayName    REG_SZ    Prism Launcher
                     */
                    String[] parts =
                            line.split("\\s{2,}", 3);

                    if (parts.length >= 3) {

                        String name =
                                parts[0];

                        String value =
                                parts[2];

                        current.put(name, value);
                    }
                }

                /*
                 * 添加最后一个 Key
                 */
                if (current != null) {
                    result.add(current);
                }
            }

            process.waitFor();

        } catch (Exception e) {

            System.err.println(
                    "查询 Windows Registry 失败："
                            + e.getMessage()
            );
        }

        return result;
    }

    /**
     * Registry Key。
     */
    public static class RegistryEntry {

        private final String key;

        private final Map<String, String> values =
                new LinkedHashMap<>();

        public RegistryEntry(String key) {
            this.key = key;
        }

        private void put(
                String name,
                String value) {

            values.put(name, value);
        }

        public String get(String name) {
            return values.get(name);
        }

        public String getKey() {
            return key;
        }

        public Map<String, String> getValues() {
            return Map.copyOf(values);
        }
    }
}