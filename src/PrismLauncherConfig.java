import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

public class PrismLauncherConfig {

    private final Path configDirectory;
    private final Path configFile;

    public PrismLauncherConfig(Path configDirectory) {

        this.configDirectory =
                configDirectory
                        .toAbsolutePath()
                        .normalize();

        this.configFile =
                this.configDirectory
                        .resolve("prismlauncher.cfg");
    }

    /**
     * 获取 InstanceDir。
     *
     * 如果 InstanceDir 是绝对路径：
     *
     * InstanceDir=C:/Minecraft/instances
     *
     * 返回：
     *
     * C:/Minecraft/instances
     *
     * 如果 InstanceDir 是相对路径：
     *
     * InstanceDir=instances
     *
     * 返回：
     *
     * C:/Users/<用户>/AppData/Roaming/PrismLauncher/instances
     */
    public Path getInstanceDir() {

        if (!Files.isRegularFile(configFile)) {
            return null;
        }

        try {

            for (String line :
                    Files.readAllLines(
                            configFile,
                            StandardCharsets.UTF_8)) {

                line = line.trim();

                // 空行
                if (line.isEmpty()) {
                    continue;
                }

                // 注释
                if (line.startsWith("#")) {
                    continue;
                }

                // InstanceDir
                if (line.startsWith("InstanceDir=")) {

                    String value =
                            line.substring(
                                    "InstanceDir=".length()
                            ).trim();

                    if (value.isEmpty()) {
                        return null;
                    }

                    return resolveInstanceDir(value);
                }
            }

        } catch (IOException e) {

            System.err.println(
                    "读取 Prism Launcher 配置失败："
                            + e.getMessage()
            );

            return null;
        }

        return null;
    }

    /**
     * 将 InstanceDir 转换成完整路径。
     */
    private Path resolveInstanceDir(String value) {

        Path path;

        try {
            path = Path.of(value);
        } catch (Exception e) {
            return null;
        }

        /*
         * Windows 下判断是否为绝对路径。
         *
         * 例如：
         *
         * C:\Minecraft\instances
         * C:/Minecraft/instances
         *
         * 都是绝对路径。
         */
        if (path.isAbsolute()) {
            return path.normalize();
        }

        /*
         * 相对路径：
         *
         * instances
         *
         * ↓
         *
         * PrismLauncher/instances
         */
        return configDirectory
                .resolve(path)
                .toAbsolutePath()
                .normalize();
    }

    public Path getConfigFile() {
        return configFile;
    }
}
