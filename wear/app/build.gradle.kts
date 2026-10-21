plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "com.godex.wear"
    // compileSdk 36 because that is the platform installed locally; minSdk 30 is
    // the Wear OS 3 baseline confirmed by the emulator gate (plan §3.1, gate 0.2).
    compileSdk = 36

    defaultConfig {
        applicationId = "com.godex.wear"
        minSdk = 30
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
        // The watch talks to one godex node; the base URL is configured at
        // runtime (DataStore) and defaults to the emulator's host loopback.
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        compose = true
    }
    packaging {
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.activity.compose)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.foundation)
    implementation(libs.androidx.compose.ui.tooling.preview)
    debugImplementation(libs.androidx.compose.ui.tooling)

    // Wear Material draws the round-screen design language used by the mockups.
    implementation(libs.androidx.wear.compose.material)
    implementation(libs.androidx.wear.compose.foundation)

    // System-shell surfaces (mockups 02 Tile / 12 ongoing activity). These are
    // plain AndroidX libraries — no Google Play services, which the target
    // China-market watch does not have.
    implementation(libs.androidx.wear.tiles)
    implementation(libs.androidx.wear.tiles.material)
    implementation(libs.androidx.wear.protolayout)
    implementation(libs.androidx.wear.protolayout.expression)
    implementation(libs.androidx.wear.ongoing)
    // Required to complete the TileService's ListenableFuture (see the version
    // catalogue note: the tiles POM declares this runtime-scope only).
    implementation(libs.androidx.concurrent.futures)

    implementation(libs.androidx.datastore.preferences)
    // Background pull so an approval can reach the wrist with the app closed.
    implementation(libs.androidx.work.runtime.ktx)
    implementation(libs.okhttp)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)

    testImplementation("junit:junit:4.13.2")
}
