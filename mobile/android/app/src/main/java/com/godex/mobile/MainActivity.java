package com.godex.mobile;

import android.content.Context;
import android.content.SharedPreferences;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.os.Bundle;
import android.system.Os;
import android.util.Log;
import android.webkit.WebView;

import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;

import com.getcapacitor.BridgeActivity;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.security.SecureRandom;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/**
 * godex Mobile 主 Activity。
 *
 * 两种模式：
 * 1. 本地模式（默认，nativeLibraryDir 含 libgodex.so 时）：从 nativeLibraryDir
 *    （SELinux 类型 app_lib_file，untrusted_app 有 exec 权限）spawn `godex serve`
 *    （127.0.0.1:17889 或空闲端口），注入随机 token，等 /meta 就绪后 WebView
 *    加载本地服务 —— agent 在 Android 本地运行（桌面版 Tauri sidecar 等价实现）。
 *    关键：二进制必须放 jniLibs 而非 assets+filesDir —— 小米 HyperOS 的 SELinux
 *    策略禁止 untrusted_app 对 app_data_file 类型文件执行 execute_no_trans
 *    （error=13 Permission denied），但 app_lib_file 类型天然可执行。
 *    busybox/git 运行时同样以 lib*.so 进 jniLibs，启动时在可写目录建符号链接
 *    指向 nativeLibraryDir，加入 PATH 供 agent 的 bash(sh -c)/git 工具使用。
 * 2. 远程模式（nativeLibraryDir 无 libgodex.so 时）：WebView 加载远端 godex 地址。
 *
 * 性能与稳定性要点：
 * - spawn 在后台线程执行，onCreate 主线程立即返回，避免 ANR。
 * - 所有回调切回 UI 线程操作 WebView；getBridge()/getWebView() 判空防护。
 * - 全局未捕获异常写 logcat + filesDir/crash.log，便于真机定位。
 */
public class MainActivity extends BridgeActivity {

    private static final String TAG = "godex-mobile";
    /** nativeLibraryDir 内 godex 二进制（jniLibs/arm64-v8a/libgodex.so）。 */
    private static final String NATIVE_GODEX = "libgodex.so";
    /** nativeLibraryDir 内运行时（jniLibs/arm64-v8a/lib*.so）。 */
    private static final String NATIVE_BUSYBOX = "libbusybox.so";
    private static final String NATIVE_GIT = "libgit.so";
    private static final String NATIVE_GIT_HTTPS = "libgitremotehttps.so";
    /** busybox 常用 applet 名：在可写目录建符号链接指向 nativeLibraryDir/libbusybox.so。
     *  注意：busybox（EXALAB 静态版）没有编译 bash applet，不建 bash 链接，
     *  否则 exec bash 报 "applet not found"（terminal 面板 resolveShell 在
     *  Android 上已回退 sh）。 */
    private static final String[] BUSYBOX_APPLETS = {
            "sh", "ash", "hush",
            "grep", "sed", "awk", "find", "cat", "ls", "cp", "mv", "rm", "mkdir",
            "chmod", "chown", "echo", "printf", "test", "xargs", "wc", "head",
            "tail", "sort", "uniq", "date", "env", "which", "true", "false",
            "touch", "pwd", "dd", "tar", "unzip", "wget", "kill", "mount", "ps"
    };
    /** git 传输 helper：git clone/fetch 通过 PATH 找 git-upload-pack 等，缺则报
     *  "git-upload-pack: inaccessible or not found"。均链接 libgit.so。 */
    private static final String[] GIT_HELPERS = {
            "git-upload-pack", "git-receive-pack", "git-upload-archive"
    };
    private static final int DEFAULT_PORT = 17889;
    private static final String KEY_TOKEN = "godex_token";
    private static final String KEY_POLL_MS = "godex_poll_ms";
    private static final String KEY_SERVER = "godex_server_url";
    private static final String KEY_LOCAL_MODE = "godex_local_mode";
    private static final String KEY_LOCAL_ERROR = "godex_local_error";

    private Process godexProcess;
    private volatile boolean destroyed = false;

    @Override
    public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        installCrashHandler();
        if (nativeGodexExists()) {
            // 本地模式：后台线程解压符号链接 + spawn + 等就绪，主线程立即返回。
            Thread worker = new Thread(new Runnable() {
                @Override
                public void run() {
                    startLocalServerInBackground();
                }
            }, "godex-local-server");
            worker.start();
        } else {
            Log.i(TAG, "nativeLibraryDir 无 libgodex.so，使用远程模式");
            getWindow().getDecorView().post(new Runnable() {
                @Override
                public void run() {
                    injectWatcherScript(loadToken());
                }
            });
        }
    }

    // ---- 本地模式：后台线程执行符号链接 + spawn + 就绪等待 ----

    private String nativeLibDir() {
        return getApplicationInfo().nativeLibraryDir;
    }

    private boolean nativeGodexExists() {
        try {
            return new File(nativeLibDir(), NATIVE_GODEX).isFile();
        } catch (Throwable t) {
            return false;
        }
    }

    private void startLocalServerInBackground() {
        try {
            File godexBin = new File(nativeLibDir(), NATIVE_GODEX);
            if (!godexBin.isFile()) {
                throw new IOException("nativeLibraryDir 缺少 " + NATIVE_GODEX + ": " + nativeLibDir());
            }
            File runtimeDir = prepareRuntimeLinks();
            if (destroyed) {
                return;
            }
            int port = pickPort();
            final String token = generateToken();
            File workdir = new File(getFilesDir(), "godex-workspace");
            if (!workdir.exists() && !workdir.mkdirs()) {
                Log.w(TAG, "创建 workdir 失败: " + workdir);
            }

            ProcessBuilder pb = new ProcessBuilder(
                    godexBin.getAbsolutePath(), "serve", "--addr", "127.0.0.1:" + port);
            pb.directory(workdir);
            pb.redirectErrorStream(true);
            // HOME 指向私有目录：godex 的 ~/.godex 落在 filesDir/.godex。
            pb.environment().put("HOME", getFilesDir().getAbsolutePath());
            pb.environment().put("GODEX_WEB_TOKEN", token);
            // PATH 注入：符号链接目录 + nativeLibraryDir + 系统 PATH。
            String sysPath = System.getenv("PATH");
            String path = runtimeDir.getAbsolutePath() + ":" + nativeLibDir()
                    + (sysPath == null ? "" : ":" + sysPath);
            pb.environment().put("PATH", path);
            Log.i(TAG, "PATH=" + path);
            // DNS 注入：Android 沙箱内 /etc/resolv.conf 指向 [::1]:53 但 netd
            // 不监听 53 端口，纯 Go resolver（CGO_ENABLED=0）无法解析域名。
            // 读取系统真实 DNS（net.dns1/net.dns2）传给 godex 自定义 resolver。
            String dnsServers = systemDnsServers();
            if (!dnsServers.isEmpty()) {
                pb.environment().put("GODEX_DNS_SERVERS", dnsServers);
                Log.i(TAG, "GODEX_DNS_SERVERS=" + dnsServers);
            }

            godexProcess = pb.start();
            drainOutput(godexProcess);

            SharedPreferences prefs = getSharedPreferences("CapacitorStorage", MODE_PRIVATE);
            prefs.edit()
                    .putString(KEY_TOKEN, token)
                    .putString(KEY_POLL_MS, "30000")
                    .putString(KEY_SERVER, "http://127.0.0.1:" + port)
                    .putString(KEY_LOCAL_MODE, "true")
                    .remove(KEY_LOCAL_ERROR)
                    .apply();

            final String baseUrl = "http://127.0.0.1:" + port;
            waitForReady(baseUrl, new Runnable() {
                @Override
                public void run() {
                    if (destroyed) {
                        return;
                    }
                    runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            injectWatcherScript(token);
                            WebView webView = getBridge() == null ? null : getBridge().getWebView();
                            if (webView != null) {
                                webView.loadUrl(baseUrl + "/");
                            }
                        }
                    });
                }
            });
        } catch (Throwable t) {
            Log.e(TAG, "启动本地 godex serve 失败，回退远程模式", t);
            writeLocalError(t);
            runOnUiThread(new Runnable() {
                @Override
                public void run() {
                    injectWatcherScript(loadToken());
                }
            });
        }
    }

    /** 把本地模式失败原因写入 Preferences，设置页可读取显示。 */
    private void writeLocalError(Throwable t) {
        try {
            String msg = String.valueOf(t.getMessage());
            StringWriter sw = new StringWriter();
            t.printStackTrace(new PrintWriter(sw));
            String stack = sw.toString();
            getSharedPreferences("CapacitorStorage", MODE_PRIVATE)
                    .edit().putString(KEY_LOCAL_ERROR, msg + "\n" + stack).apply();
            writeCrashLog("本地模式失败: " + msg + "\n" + stack);
        } catch (Throwable ignored) {
        }
    }

    /**
     * 在 filesDir/runtime 建符号链接指向 nativeLibraryDir 的 lib*.so：
     *  - busybox applet（sh/grep/sed/...）→ libbusybox.so
     *  - git → libgit.so；git-remote-https → libgitremotehttps.so
     * 符号链接目标为 app_lib_file 类型，untrusted_app 可 exec。
     */
    private File prepareRuntimeLinks() {
        File runtimeDir = new File(getFilesDir(), "runtime");
        if (!runtimeDir.exists() && !runtimeDir.mkdirs()) {
            Log.w(TAG, "创建 runtime 目录失败: " + runtimeDir);
        }
        String libDir = nativeLibDir();
        File busybox = new File(libDir, NATIVE_BUSYBOX);
        if (busybox.isFile()) {
            for (String applet : BUSYBOX_APPLETS) {
                symlinkTo(libDir, NATIVE_BUSYBOX, new File(runtimeDir, applet));
            }
        }
        symlinkTo(libDir, NATIVE_GIT, new File(runtimeDir, "git"));
        for (String helper : GIT_HELPERS) {
            symlinkTo(libDir, NATIVE_GIT, new File(runtimeDir, helper));
        }
        symlinkTo(libDir, NATIVE_GIT_HTTPS, new File(runtimeDir, "git-remote-https"));
        return runtimeDir;
    }

    /** 确保 link -> nativeLibraryDir/targetName，处理重装后路径漂移。 */
    private void symlinkTo(String libDir, String targetName, File link) {
        File target = new File(libDir, targetName);
        if (!target.isFile()) {
            Log.w(TAG, "nativeLibraryDir 缺少 " + targetName);
            return;
        }
        try {
            if (Files.isSymbolicLink(link.toPath())) {
                // 目标漂移检测：install -r 后 nativeLibraryDir 哈希路径会变化，
                // 旧符号链接成为 dangling link（link.exists() 返回 false）。
                java.nio.file.Path cur = Files.readSymbolicLink(link.toPath());
                if (cur.toString().equals(target.getAbsolutePath())) {
                    return; // 已指向当前目标，跳过
                }
                if (!link.delete()) {
                    Log.w(TAG, "删除旧符号链接失败 " + link.getName());
                    return;
                }
            } else if (link.exists()) {
                if (!link.delete()) {
                    Log.w(TAG, "删除旧占位文件失败 " + link.getName());
                    return;
                }
            }
            Os.symlink(target.getAbsolutePath(), link.getAbsolutePath());
        } catch (Exception e) {
            Log.w(TAG, "创建符号链接失败 " + link.getName() + ": " + e.getMessage());
        }
    }

    /** 选端口：默认 17889，被占用则取空闲端口（与桌面版 pick_port 等价）。 */
    private int pickPort() {
        try (ServerSocket probe = new ServerSocket(DEFAULT_PORT, 50,
                java.net.InetAddress.getByName("127.0.0.1"))) {
            return DEFAULT_PORT;
        } catch (IOException e) {
            try (ServerSocket free = new ServerSocket(0, 50,
                    java.net.InetAddress.getByName("127.0.0.1"))) {
                return free.getLocalPort();
            } catch (IOException e2) {
                return DEFAULT_PORT;
            }
        }
    }

    private String generateToken() {
        byte[] buf = new byte[16];
        new SecureRandom().nextBytes(buf);
        StringBuilder sb = new StringBuilder();
        for (byte b : buf) {
            sb.append(String.format("%02x", b));
        }
        return sb.toString();
    }

    /** 后台轮询 /meta 直到就绪（25s 超时），随后回调（主线程执行）。 */
    private void waitForReady(final String baseUrl, final Runnable onReady) {
        final Thread t = new Thread(new Runnable() {
            @Override
            public void run() {
                long deadline = System.currentTimeMillis() + 25_000;
                while (System.currentTimeMillis() < deadline && !destroyed) {
                    if (isMetaOk(baseUrl)) {
                        onReady.run();
                        return;
                    }
                    try {
                        Thread.sleep(500);
                    } catch (InterruptedException e) {
                        return;
                    }
                }
                Log.e(TAG, "godex serve 未在 25s 内就绪: " + baseUrl);
            }
        });
        t.setDaemon(true);
        t.start();
    }

    private boolean isMetaOk(String baseUrl) {
        try {
            HttpURLConnection conn = (HttpURLConnection) new URL(baseUrl + "/meta").openConnection();
            conn.setConnectTimeout(1500);
            conn.setReadTimeout(1500);
            int code = conn.getResponseCode();
            conn.disconnect();
            return code == 200;
        } catch (IOException e) {
            return false;
        }
    }

    /** 消费子进程输出，避免管道写满阻塞。 */
    private void drainOutput(final Process p) {
        final Thread t = new Thread(new Runnable() {
            @Override
            public void run() {
                try (BufferedReader br = new BufferedReader(
                        new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                    String line;
                    while ((line = br.readLine()) != null) {
                        Log.i(TAG, "[godex] " + line);
                    }
                } catch (IOException e) {
                    // 进程退出时忽略
                }
            }
        });
        t.setDaemon(true);
        t.start();
    }

    @Override
    public void onDestroy() {
        destroyed = true;
        super.onDestroy();
        if (godexProcess != null) {
            godexProcess.destroy();
            godexProcess = null;
        }
    }

    // ---- 崩溃日志 ----

    /** 全局未捕获异常：写 logcat + filesDir/crash.log，便于真机定位闪退。 */
    private void installCrashHandler() {
        final Thread.UncaughtExceptionHandler prev = Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler(new Thread.UncaughtExceptionHandler() {
            @Override
            public void uncaughtException(Thread t, Throwable e) {
                try {
                    StringWriter sw = new StringWriter();
                    e.printStackTrace(new PrintWriter(sw));
                    String stack = sw.toString();
                    Log.e(TAG, "CRASH on thread " + t.getName() + ":\n" + stack);
                    writeCrashLog(stack);
                } catch (Throwable ignored) {
                }
                if (prev != null) {
                    prev.uncaughtException(t, e);
                } else {
                    android.os.Process.killProcess(android.os.Process.myPid());
                }
            }
        });
    }

    private void writeCrashLog(String stack) {
        try {
            File f = new File(getFilesDir(), "crash.log");
            String ts = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(new Date());
            String content = "==== " + ts + " ====\n" + stack + "\n";
            try (FileOutputStream fos = new FileOutputStream(f, true)) {
                fos.write(content.getBytes(StandardCharsets.UTF_8));
            }
            Log.i(TAG, "崩溃日志已写入: " + f.getAbsolutePath());
        } catch (Throwable ignored) {
        }
    }

    // ---- watcher 注入（两种模式共用） ----

    private String loadToken() {
        SharedPreferences prefs = getSharedPreferences("CapacitorStorage", MODE_PRIVATE);
        return prefs.getString(KEY_TOKEN, "");
    }

    private void injectWatcherScript(String token) {
        try {
            if (!WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
                return;
            }
            if (getBridge() == null) {
                Log.w(TAG, "bridge 未就绪，跳过 watcher 注入");
                return;
            }
            WebView webView = getBridge().getWebView();
            if (webView == null) {
                Log.w(TAG, "webView 未就绪，跳过 watcher 注入");
                return;
            }
            SharedPreferences prefs = getSharedPreferences("CapacitorStorage", MODE_PRIVATE);
            String pollMs = prefs.getString(KEY_POLL_MS, "30000");

            String template = readAsset("public/godex-watcher.js");
            if (template == null) {
                return;
            }
            String source = template
                    .replace("__GODEX_TOKEN__", escapeForJS(token))
                    .replace("__GODEX_POLL_MS__", escapeForJS(pollMs));
            // Android 15+ 强制 edge-to-edge：状态栏透明且 WebView 内容绘制到其下方。
            // WebView 的 env(safe-area-inset-top) 支持不可靠，故在 document-start
            // 注入真实状态栏高度到 CSS 变量 --godex-safe-top，Web UI 顶栏据此下移。
            String safeTopCss = "(function(){try{document.documentElement.style.setProperty('--godex-safe-top','"
                    + statusBarHeightPx() + "px');}catch(e){}})();";
            source = safeTopCss + "\n" + source;
            WebViewCompat.addDocumentStartJavaScript(
                    webView, source, java.util.Collections.singleton("*"));
        } catch (Throwable t) {
            Log.e(TAG, "watcher 注入失败（不影响主流程）", t);
        }
    }

    /** Android 状态栏高度（CSS px = dp），失败时回退 0（桌面/无状态栏场景）。 */
    private int statusBarHeightPx() {
        try {
            int id = getResources().getIdentifier("status_bar_height", "dimen", "android");
            if (id > 0) {
                // getDimension 返回物理 px；WebView CSS px == dp，需除以 density。
                float px = getResources().getDimension(id);
                float density = getResources().getDisplayMetrics().density;
                return density > 0 ? Math.round(px / density) : 0;
            }
        } catch (Throwable ignored) {
        }
        return 0;
    }

    /** 读取系统真实 DNS（net.dns1/net.dns2 系统属性），逗号分隔；读不到返回空。 */
    private String systemDnsServers() {
        try {
            StringBuilder sb = new StringBuilder();
            // 反射读取系统属性（无需额外权限，Android 上 net.* 为公共属性）。
            Class<?> sp = Class.forName("android.os.SystemProperties");
            java.lang.reflect.Method get = sp.getMethod("get", String.class);
            for (String key : new String[]{"net.dns1", "net.dns2"}) {
                String v = (String) get.invoke(null, key);
                if (v != null && !v.isEmpty()) {
                    if (sb.length() > 0) sb.append(",");
                    sb.append(v);
                }
            }
            // 回退：ConnectivityManager 的 LinkProperties（需 ACCESS_NETWORK_STATE）。
            if (sb.length() == 0) {
                try {
                    ConnectivityManager cm = (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
                    if (cm != null) {
                        Network n = cm.getActiveNetwork();
                        LinkProperties lp = n != null ? cm.getLinkProperties(n) : null;
                        if (lp != null) {
                            for (InetAddress a : lp.getDnsServers()) {
                                String h = a.getHostAddress();
                                if (h != null && !h.isEmpty()) {
                                    if (sb.length() > 0) sb.append(",");
                                    sb.append(h);
                                }
                            }
                        }
                    }
                } catch (Throwable ignored) {
                }
            }
            return sb.toString();
        } catch (Throwable t) {
            return "";
        }
    }

    /** 读取 assets 内文本文件（cap sync 把 www/ 同步到 assets/public/）。 */
    private String readAsset(String path) {
        StringBuilder sb = new StringBuilder();
        try (InputStream is = getAssets().open(path);
             BufferedReader reader = new BufferedReader(new InputStreamReader(is, StandardCharsets.UTF_8))) {
            String line;
            while ((line = reader.readLine()) != null) {
                sb.append(line).append('\n');
            }
            return sb.toString();
        } catch (IOException e) {
            Log.e(TAG, "read asset failed: " + path, e);
            return null;
        }
    }

    /** 将任意字符串安全地嵌入 JS 双引号字符串字面量。 */
    private static String escapeForJS(String raw) {
        return raw
                .replace("\\", "\\\\")
                .replace("\"", "\\\"")
                .replace("\n", "\\n")
                .replace("\r", "\\r");
    }
}
