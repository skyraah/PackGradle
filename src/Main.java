import java.nio.file.Path;

public class Main {

    public static void main(String[] args) {

        PrismLauncherDetector detector =
                new PrismLauncherDetector();

        Path instanceDir = detector.findInstanceDir();

        if (instanceDir == null) {
            System.out.println("未检测到 Prism Launcher 或 InstanceDir。");
            return;
        }

        System.out.println("Prism Launcher 已安装。");
        System.out.println("InstanceDir：");
        System.out.println(instanceDir);
    }
}
