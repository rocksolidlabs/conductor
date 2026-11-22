# Add project specific ProGuard rules here.
# You can control the set of applied configuration files using the
# proguardFiles setting in build.gradle.
#
# For more details, see
#   http://developer.android.com/guide/developing/tools/proguard.html

# If your project uses WebView with JS, uncomment the following
# and specify the fully qualified class name to the JavaScript interface
# class:
#-keepclassmembers class fqcn.of.javascript.interface.for.webview {
#   public *;
#}

# Uncomment this to preserve the line number information for
# debugging stack traces.
#-keepattributes SourceFile,LineNumberTable

# If you keep the line number information, uncomment this to
# hide the original source file name.
#-renamesourcefileattribute SourceFile

# The shell's bridge gate forwards what it admits to the native runtime's own message handler, which the runtime keeps in a private
# field: a build that shrinks keeps the field under its name (the gate refuses to stand without it, and the shell then loads nothing).
-keepclassmembers class com.getcapacitor.Bridge {
    private com.getcapacitor.MessageHandler msgHandler;
}

# A packaging's signing policy is found by its fixed name at start: a build that shrinks keeps it and its constructor.
-keep class dev.conductor.mobile.packaging.ConductorKeySigningPolicy {
    public <init>();
    *;
}
