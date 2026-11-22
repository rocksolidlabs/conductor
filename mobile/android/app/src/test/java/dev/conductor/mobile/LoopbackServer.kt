package dev.conductor.mobile

import java.io.BufferedReader
import java.io.IOException
import java.io.InputStreamReader
import java.net.InetAddress
import java.net.ServerSocket
import java.util.Collections

/**
 * A server of the tests' own on a loopback socket (the JDK's own server is not on an Android module's unit-test classpath): each
 * connection's request is read to its blank line and recorded (its path and its headers, by their names in lower case), answered with
 * the bytes `answer` gives for its path, and closed. The listener is kept for the test's length, accepting on a thread of its own;
 * `close` ends it and waits for that thread.
 */
class LoopbackServer(private val answer: (String) -> ByteArray) : AutoCloseable {
    private val socket = ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"))
    val port: Int = socket.localPort
    val base = "http://127.0.0.1:$port"

    /** Every request read, in order: its headers, and its path under ":path". */
    val requests: MutableList<Map<String, String>> = Collections.synchronizedList(mutableListOf())

    fun paths(): List<String> = synchronized(requests) { requests.map { it.getValue(":path") } }

    private val loop = Thread {
        while (!socket.isClosed) {
            val connection = try {
                socket.accept()
            } catch (e: IOException) {
                break
            }
            try {
                connection.use { c ->
                    val input = BufferedReader(InputStreamReader(c.getInputStream(), Charsets.ISO_8859_1))
                    val first = input.readLine() ?: return@use
                    val headers = linkedMapOf<String, String>()
                    while (true) {
                        val line = input.readLine() ?: break
                        if (line.isEmpty()) break
                        val colon = line.indexOf(':')
                        if (colon > 0) headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
                    }
                    val path = first.split(" ").getOrElse(1) { "/" }
                    requests.add(headers + (":path" to path))
                    val out = c.getOutputStream()
                    out.write(answer(path))
                    out.flush()
                }
            } catch (e: IOException) {
                // A client that left before the whole answer (a bound it applied): the next connection is served all the same.
            }
        }
    }.apply {
        isDaemon = true
        start()
    }

    override fun close() {
        socket.close()
        loop.join(10_000)
    }

    companion object {
        /** An HTTP/1.1 answer with its status, its body and the headers given, closing the connection after it. */
        fun reply(status: Int, body: String, vararg headers: String): ByteArray {
            val bytes = body.toByteArray(Charsets.UTF_8)
            val head = StringBuilder("HTTP/1.1 $status ${if (status == 200) "OK" else "Status"}\r\n")
            for (h in headers) head.append(h).append("\r\n")
            head.append("Content-Length: ${bytes.size}\r\nConnection: close\r\n\r\n")
            return head.toString().toByteArray(Charsets.ISO_8859_1) + bytes
        }
    }
}
