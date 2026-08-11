import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.List;

public class PrismLauncherDetector {

    private static final String PRISM_LAUNCHER_NAME =
            "prism launcher";

    private static final String CONFIG_FILE =
            "prismlauncher.cfg";

    private static final String DEFAULT_CONFIG_DIR =
            "PrismLauncher";

    private final WindowsRegistry registry;

    public PrismLauncherDetector() {
        this.registry = new WindowsRegistry();
    }

    /**
     * 查找 Prism Launcher 的实例目录。
     *
     * @return InstanceDir 的完整路径
     *         找不到返回 null
     */
    public Path findInstanceDir() {

        // 第一步：确认 Prism Launcher 是否安装
        if (!isPrismLauncherInstalled()) {
            return null;
        }

        // 第二步：找到 Prism Launcher 配置目录
        Path configDirectory =
                getPrismLauncherConfigDirectory();

        if (configDirectory == null) {
            return null;
        }

        // 第三步：读取 InstanceDir
        PrismLauncherConfig config =
                new PrismLauncherConfig(configDirectory);

        return config.getInstanceDir();
    }

    /**
     * 判断 Prism Launcher 是否安装。
     */
    private boolean isPrismLauncherInstalled() {

        List<String> registryPaths = List.of(
                "HKCU\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall",
                "HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Uninstall",
                "HKLM\\SOFTWARE\\WOW6432Node\\Microsoft\\Windows\\CurrentVersion\\Uninstall"
        );

        for (String registryPath : registryPaths) {

            List<WindowsRegistry.RegistryEntry> entries =
                    registry.query(registryPath);

            for (WindowsRegistry.RegistryEntry entry : entries) {

                String displayName =
                        entry.get("DisplayName");

                if (displayName == null) {
                    continue;
                }

                if (!displayName
                        .toLowerCase()
                        .contains(PRISM_LAUNCHER_NAME)) {
                    continue;
                }

                return true;
            }
        }

        return false;
    }

    /**
     * 获取当前用户的 Prism Launcher 配置目录。
     *
     * Windows:
     *
     * C:\Users\<用户名>\AppData\Roaming\PrismLauncher
     */
    private Path getPrismLauncherConfigDirectory() {

        String appData =
                System.getenv("APPDATA");

        if (appData == null || appData.isBlank()) {
            return null;
        }

        Path directory =
                Paths.get(appData)
                        .resolve(DEFAULT_CONFIG_DIR)
                        .toAbsolutePath()
                        .normalize();

        if (!Files.isDirectory(directory)) {
            return null;
        }

        return directory;
    }
}