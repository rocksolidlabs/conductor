package dev.conductor.mobile

import android.os.Bundle
import com.getcapacitor.BridgeActivity

// The Android shell's activity: the native runtime's own, with the shell's plugins registered before its bridge is made. The shell's
// gates are put in front of it in a commit of their own.
class MainActivity : BridgeActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        registerPlugin(ConductorStorePlugin::class.java)
        super.onCreate(savedInstanceState)
    }
}
